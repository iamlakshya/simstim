/* SPDX-License-Identifier: Apache-2.0 */
#include "copy.h"
#include <errno.h>
#include <fcntl.h>
#include <poll.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/prctl.h>
#include <sys/socket.h>
#include <sys/syscall.h>
#include <sys/wait.h>
#include <unistd.h>
static bool transfer(int fd,void *data,size_t size,bool write_data) {
    size_t offset=0;
    while (offset<size) {
        ssize_t n=write_data ? send(fd,(char *)data+offset,size-offset,MSG_NOSIGNAL):read(fd,(char *)data+offset,size-offset);
        if (n<0 && errno==EINTR) continue;if (n<=0) return false;offset+=(size_t)n;
    }
    return true;
}
static uint32_t get32(const unsigned char *bytes) {return (uint32_t)bytes[0]|(uint32_t)bytes[1]<<8|(uint32_t)bytes[2]<<16|(uint32_t)bytes[3]<<24;}
static void status(char kind,int error) {
    uint32_t code=(uint32_t)error;unsigned char bytes[5]={(unsigned char)kind,(unsigned char)code,(unsigned char)(code>>8),(unsigned char)(code>>16),(unsigned char)(code>>24)};
    if (!transfer(6,bytes,sizeof(bytes),true)) fail("reply on original copy channel");
}
static bool beneath(const char *path,const char *root) {size_t n=strlen(root);return !strncmp(path,root,n) && (path[n]==0 || path[n]=='/');}
static struct copy_mount *select_mount(struct copy_owner *owner,const char *target) {
    if (!(beneath(target,"/work") || beneath(target,"/home/agent") || beneath(target,"/tmp") || beneath(target,"/mnt"))) return NULL;
    const struct mount_request *selected=NULL;
    for (size_t i=0;i<owner->request->count;++i) {
        const struct mount_request *mount=&owner->request->mounts[i];
        if (beneath(target,mount->guest) && (!selected || strlen(mount->guest)>strlen(selected->guest))) selected=mount;
    }
    if (selected && !selected->write) return NULL;
    const char *prefix=selected ? selected->guest:"/";
    for (size_t i=0;i<owner->count;++i) if (!strcmp(owner->mounts[i].prefix,prefix)) return &owner->mounts[i];
    return NULL;
}
static int cleanup(struct copy_transaction *tx) {
    if (tx->file<0) return 0;
    struct stat current;int result=0;
    if (!fstatat(tx->mount->stage,tx->name,&current,AT_SYMLINK_NOFOLLOW)) {
        if (current.st_dev!=tx->identity.st_dev || current.st_ino!=tx->identity.st_ino) result=ESTALE;
        else if (unlinkat(tx->mount->stage,tx->name,0)) result=errno;
    } else if (errno!=ENOENT) result=errno;
    if (close(tx->file) && !result) result=errno;tx->file=-1;return result;
}
static int begin(struct copy_owner *owner,struct copy_transaction *tx,uint64_t *sequence) {
    unsigned char header[5];if (!transfer(6,header,sizeof(header),false)) return EPIPE;
    uint32_t length=get32(header+1);
    if (header[0]>1 || !length || length>=sizeof(tx->target)) return EPROTO;
    if (!transfer(6,tx->target,length,false)) return EPIPE;tx->target[length]=0;
    if (tx->file>=0 || strlen(tx->target)!=length || !valid_guest_path(tx->target)) return EPROTO;
    tx->mount=select_mount(owner,tx->target);if (!tx->mount) return EACCES;
    if (!strcmp(tx->target,tx->mount->prefix)) return EISDIR;
    tx->replace=header[0]!=0;
    if (*sequence==UINT64_MAX) return EOVERFLOW;
    int n=snprintf(tx->name,sizeof(tx->name),"file-%llu",(unsigned long long)++*sequence);
    if (n<0 || (size_t)n>=sizeof(tx->name)) return EOVERFLOW;
    tx->file=openat(tx->mount->stage,tx->name,O_CREAT|O_EXCL|O_RDWR|O_NOFOLLOW|O_CLOEXEC,0600);
    if (tx->file<0) return errno;
    if (fstat(tx->file,&tx->identity)) {int e=errno;close(tx->file);tx->file=-1;return e;}
    return 0;
}
static void stage(struct copy_transaction *tx) {
    int fd=tx->file;
    char byte='S';struct iovec io={.iov_base=&byte,.iov_len=1};
    union {struct cmsghdr align;char bytes[CMSG_SPACE(sizeof(int))];} control={0};
    struct msghdr message={.msg_iov=&io,.msg_iovlen=1,.msg_control=control.bytes,.msg_controllen=sizeof(control.bytes)};
    struct cmsghdr *c=CMSG_FIRSTHDR(&message);c->cmsg_level=SOL_SOCKET;c->cmsg_type=SCM_RIGHTS;c->cmsg_len=CMSG_LEN(sizeof(int));memcpy(CMSG_DATA(c),&fd,sizeof(fd));
    if (sendmsg(6,&message,MSG_NOSIGNAL)!=1) fail("transfer private original-mount copy stage");
    uint32_t index=tx->mount->index;unsigned char detail[52]={0};
    detail[0]=(unsigned char)index;detail[1]=(unsigned char)(index>>8);detail[2]=(unsigned char)(index>>16);detail[3]=(unsigned char)(index>>24);
    memcpy(detail+4,tx->name,strlen(tx->name)+1);
    if (!transfer(6,detail,sizeof(detail),true)) fail("transfer exact owned copy stage identity");
}
static int publish(struct copy_owner *owner,struct copy_transaction *tx,int init_pidfd,volatile sig_atomic_t *stopping,bool *cancelled) {
    int result[2],gate[2];if (pipe2(result,O_CLOEXEC)) return errno;
    if (pipe2(gate,O_CLOEXEC)) {int e=errno;close(result[0]);close(result[1]);return e;}
    /* The outer process did NOT unshare its pid-for-children namespace. This
     * fork is outside the guest PID namespace; guest PID1 cannot signal it. */
    pid_t pid=fork();if (pid<0) {int e=errno;close(result[0]);close(result[1]);close(gate[0]);close(gate[1]);return e;}
    if (!pid) {
        close(result[0]);close(gate[1]);close(6);
        int code=copy_publish(owner,tx,gate[0]);
        ssize_t n=write(result[1],&code,sizeof(code));_exit(n==(ssize_t)sizeof(code) ? 0:1);
    }
    close(result[1]);close(gate[0]);
    int pidfd=(int)syscall(SYS_pidfd_open,pid,0);
    if (pidfd<0) {int e=errno;kill(pid,SIGKILL);while (waitpid(pid,NULL,0)<0 && errno==EINTR) {}close(result[0]);close(gate[1]);return e;}
    bool failed=false;
    /* The supervisor receives the SAME unreaped publisher pidfd before its
     * gate opens. On outer-owner death it can prove actual publisher exit. */
    char owned='P';struct iovec io={.iov_base=&owned,.iov_len=1};
    union {struct cmsghdr align;char bytes[CMSG_SPACE(sizeof(int))];} control={0};
    struct msghdr message={.msg_iov=&io,.msg_iovlen=1,.msg_control=control.bytes,.msg_controllen=sizeof(control.bytes)};
    struct cmsghdr *c=CMSG_FIRSTHDR(&message);c->cmsg_level=SOL_SOCKET;c->cmsg_type=SCM_RIGHTS;c->cmsg_len=CMSG_LEN(sizeof(int));memcpy(CMSG_DATA(c),&pidfd,sizeof(pidfd));
    if (sendmsg(6,&message,MSG_NOSIGNAL)!=1) {failed=true;*stopping=1;}
    char ack=0;
    if (!failed && read(6,&ack,1)==1 && ack=='X') {
        *cancelled=true;
        /* The physical reader always acknowledges P, even during cancel. */
        if (read(6,&ack,1)!=1) failed=true;
    }
    if (ack!='p') failed=true;
    if (*cancelled || failed || *stopping || write(gate[1],"A",1)!=1) syscall(SYS_pidfd_send_signal,pidfd,SIGKILL,NULL,0);
    for (;;) {
        struct pollfd pending[3]={{.fd=pidfd,.events=POLLIN},{.fd=6,.events=POLLIN},{.fd=init_pidfd,.events=POLLIN}};
        int n=(*cancelled || failed) ? 0:poll(pending,3,-1);
        if (*cancelled || failed || *stopping || n<0 || pending[2].revents) {syscall(SYS_pidfd_send_signal,pidfd,SIGKILL,NULL,0);failed=true;break;}
        if (pending[1].revents) {
            char command;if (read(6,&command,1)!=1 || command!='X') {failed=true;*stopping=1;}
            else *cancelled=true;
            syscall(SYS_pidfd_send_signal,pidfd,SIGKILL,NULL,0);break;
        }
        if (pending[0].revents) break;
    }
    int state;while (waitpid(pid,&state,0)<0) {if (errno==EINTR) continue;fail("join original copy publisher");}
    close(pidfd);close(gate[1]);int code=EIO;
    ssize_t n=read(result[0],&code,sizeof(code));close(result[0]);
    if (failed || !WIFEXITED(state) || WEXITSTATUS(state)!=0 || n!=(ssize_t)sizeof(code)) code=EIO;
    return *cancelled ? ECANCELED:code;
}
int copy_serve(struct copy_owner *owner,int init_pidfd,pid_t init_pid,volatile sig_atomic_t *stopping) {
    struct copy_transaction tx={.file=-1};uint64_t sequence=0;int state=0;
    for (;;) {
        struct pollfd pending[2]={{.fd=6,.events=POLLIN},{.fd=init_pidfd,.events=POLLIN}};
        int n=poll(pending,2,-1);
        if (*stopping || (n<0 && errno!=EINTR)) {syscall(SYS_pidfd_send_signal,init_pidfd,SIGKILL,NULL,0);break;}
        if (n<0) continue;
        if (pending[1].revents) break;
        if (!pending[0].revents) continue;
        char command;if (read(6,&command,1)!=1) {*stopping=1;syscall(SYS_pidfd_send_signal,init_pidfd,SIGKILL,NULL,0);break;}
        if (command=='B') {
            int e=begin(owner,&tx,&sequence);
            if (e==EPROTO || e==EPIPE) {*stopping=1;syscall(SYS_pidfd_send_signal,init_pidfd,SIGKILL,NULL,0);break;}
            if (e) status('E',e);else stage(&tx);
        } else if (command=='X') {int e=cleanup(&tx);status('A',e);}
        else if (command=='C' && tx.file>=0) {
            bool cancelled=false;int e=publish(owner,&tx,init_pidfd,stopping,&cancelled),clean=cleanup(&tx);if (!e) e=clean;
            if (!*stopping) {status('A',e);if (cancelled) status('A',clean);}
        } else {*stopping=1;syscall(SYS_pidfd_send_signal,init_pidfd,SIGKILL,NULL,0);break;}
    }
    if (cleanup(&tx)) fail("clean exact original copy stage");
    while (waitpid(init_pid,&state,0)<0) {if (errno==EINTR) continue;fail("join namespace init after copy owner shutdown");}
    return state;
}
