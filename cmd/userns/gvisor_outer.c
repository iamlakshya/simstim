/* SPDX-License-Identifier: Apache-2.0 */
#include "launcher.h"
#include <errno.h>
#include <fcntl.h>
#include <grp.h>
#include <linux/capability.h>
#include <linux/sched.h>
#include <poll.h>
#include <sched.h>
#include <signal.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/prctl.h>
#include <sys/resource.h>
#include <sys/socket.h>
#include <sys/stat.h>
#include <sys/syscall.h>
#include <sys/sysmacros.h>
#include <sys/wait.h>
#include <unistd.h>
#include "policy.h"

/* This role accepts no command, setup, environment, source, runtime, rule,
 * session or operation rights. The original Go constructor alone verifies the
 * public helper capability and later lends private rights after its actual
 * post-exec ND0 observation. The helper's new closed stage owns that handshake. */
static void outer_map(const char *path,unsigned outside) {
    char text[80];int n=snprintf(text,sizeof(text),"0 %u 1\n",outside);
    int fd=open(path,O_WRONLY|O_NOFOLLOW|O_CLOEXEC);
    if (fd<0 || write(fd,text,(size_t)n)!=n || close(fd)) fail("write closed gvisor outer ID map");
}
static void check_map(const char *path,unsigned outside) {
    FILE *file=fopen(path,"re");unsigned inside,host,count;char extra;
    if (!file || fscanf(file,"%u %u %u",&inside,&host,&count)!=3 || inside || host!=outside || count!=1 || fscanf(file," %c",&extra)!=EOF || ferror(file)) fail("verify closed gvisor outer ID map");
    if (fclose(file)) fail("close verified outer ID map");
}
static void null_stdio(void) {
    int fd=open("/dev/null",O_RDWR|O_NOFOLLOW|O_CLOEXEC);struct stat info;
    if (fd<0 || fstat(fd,&info) || !S_ISCHR(info.st_mode) || major(info.st_rdev)!=1 || minor(info.st_rdev)!=3) fail("pin fixed bootstrap null stdio");
    for (int i=0;i<3;++i) if (fd!=i && dup3(fd,i,0)<0) fail("install fixed bootstrap null stdio");
    if (fd>2) close(fd);
}
static void close_future_slots(void) {
    if (!syscall(SYS_close_range,5U,~0U,0)) return;
    if (errno!=ENOSYS && errno!=EINVAL) fail("close gvisor pre-authority descriptor slots");
    struct rlimit limit;if (getrlimit(RLIMIT_NOFILE,&limit)) fail("inspect bootstrap descriptor range");
    for (rlim_t fd=5;fd<limit.rlim_cur;++fd) close((int)fd);
}
static _Noreturn void owned_failure(int pidfd,pid_t child,const char *what) {
    (void)syscall(SYS_pidfd_send_signal,pidfd,SIGKILL,NULL,0);
    while (waitpid(child,NULL,0)<0) {if (errno==EINTR) continue;fail("join failed closed gvisor outer child");}
    close(pidfd);fail(what);
}
static bool native_frame(int channel,unsigned char tag,pid_t child,int descriptor) {
    uint32_t pid=(uint32_t)child;size_t payload=(tag=='P' || tag=='F') ? 5:1;
    unsigned char data[10]={(unsigned char)(payload+1),0,0,0,NATIVE_TRESPONSE,tag,(unsigned char)pid,(unsigned char)(pid>>8),(unsigned char)(pid>>16),(unsigned char)(pid>>24)};
    size_t size=5+payload;
    struct iovec io={.iov_base=data,.iov_len=size};
    union {struct cmsghdr align;char bytes[CMSG_SPACE(sizeof(int))];} control={0};
    struct msghdr message={.msg_iov=&io,.msg_iovlen=1};
    if (descriptor>=0) {
        message.msg_control=control.bytes;message.msg_controllen=sizeof(control.bytes);
        struct cmsghdr *c=CMSG_FIRSTHDR(&message);c->cmsg_level=SOL_SOCKET;c->cmsg_type=SCM_RIGHTS;c->cmsg_len=CMSG_LEN(sizeof(int));memcpy(CMSG_DATA(c),&descriptor,sizeof(descriptor));
    }
    ssize_t n;do {n=sendmsg(channel,&message,MSG_NOSIGNAL);} while (n<0 && errno==EINTR);
    if (n<=0) return false;
    size_t done=(size_t)n;
    while (done<size) {
        n=send(channel,data+done,size-done,MSG_NOSIGNAL);
        if (n<0 && errno==EINTR) continue;
        if (n<=0) return false;
        done+=(size_t)n;
    }
    return true;
}
static bool activation(int channel,pid_t creator,uid_t uid,gid_t gid,volatile sig_atomic_t *stopping,uint32_t *slots) {
    const unsigned char expected[6]={6,0,0,0,NATIVE_TREQUEST,'G'};
    unsigned char bytes[10]={0};size_t done=0;
    while (done<sizeof(bytes)) {
        struct pollfd pending={.fd=channel,.events=POLLIN};int ready;
        do {ready=poll(&pending,1,30000);} while (ready<0 && errno==EINTR && !*stopping);
        if (*stopping || ready!=1 || !(pending.revents&POLLIN)) return false;
        struct iovec io={.iov_base=bytes+done,.iov_len=sizeof(bytes)-done};
        union {struct cmsghdr align;char bytes[CMSG_SPACE(sizeof(struct ucred))+CMSG_SPACE(sizeof(int))];} control={0};
        struct msghdr message={.msg_iov=&io,.msg_iovlen=1,.msg_control=control.bytes,.msg_controllen=sizeof(control.bytes)};
        ssize_t n=recvmsg(channel,&message,MSG_CMSG_CLOEXEC);
        bool valid=n>0 && !(message.msg_flags&(MSG_TRUNC|MSG_CTRUNC)),seen=false;
        size_t cursor=0;struct cmsghdr *c;
        while ((c=native_control_next(&message,&cursor))!=NULL) {
            if (c->cmsg_level==SOL_SOCKET && c->cmsg_type==SCM_CREDENTIALS && c->cmsg_len==CMSG_LEN(sizeof(struct ucred)) && !seen) {
                struct ucred current;memcpy(&current,CMSG_DATA(c),sizeof(current));seen=true;
                if (current.pid!=creator || current.uid!=uid || current.gid!=gid) valid=false;
            } else {
                valid=false;
                if (c->cmsg_level==SOL_SOCKET && c->cmsg_type==SCM_RIGHTS && c->cmsg_len>=CMSG_LEN(0)) {
                    size_t count=(c->cmsg_len-CMSG_LEN(0))/sizeof(int);int *received=(int *)CMSG_DATA(c);
                    for (size_t i=0;i<count;++i) close(received[i]);
                }
            }
        }
        if (!valid || !seen) return false;
        done+=(size_t)n;
    }
    if (memcmp(bytes,expected,sizeof(expected))) return false;
    *slots=(uint32_t)bytes[6]|(uint32_t)bytes[7]<<8|(uint32_t)bytes[8]<<16|(uint32_t)bytes[9]<<24;
    return *slots>=6 && *slots<=1024;
}
static _Noreturn void mapped_child(uid_t uid,gid_t gid,int gate) {
    if (prctl(PR_SET_PDEATHSIG,SIGKILL,0,0,0)) fail("arm closed outer child parent death");
    unsigned char start[5];
    if (read(gate,start,sizeof(start))!=(ssize_t)sizeof(start) || start[0]!='G') fail("original creator ended before gvisor map activation");
    uint32_t slots=(uint32_t)start[1]|(uint32_t)start[2]<<8|(uint32_t)start[3]<<16|(uint32_t)start[4]<<24;
    if (slots<6 || slots>1024) fail("closed outer future slot count is invalid");
    /* CLONE_NEWUSER grants only namespace-local setup capabilities. Empty the
     * inherited host supplementary groups while this first namespace still
     * permits changes, before either its deny file or gid map is written. */
    if (setgroups(0,NULL)) fail("clear actual outer supplementary groups before first deny");
    outer_map("/proc/self/uid_map",uid);
    int groups=open("/proc/self/setgroups",O_WRONLY|O_NOFOLLOW|O_CLOEXEC);
    if (groups<0 || write(groups,"deny\n",5)!=5 || close(groups)) fail("deny later outer group changes");
    outer_map("/proc/self/gid_map",gid);
    uid_t real_uid,effective_uid,saved_uid;gid_t real_gid,effective_gid,saved_gid;
    if (getresuid(&real_uid,&effective_uid,&saved_uid) || getresgid(&real_gid,&effective_gid,&saved_gid) || real_uid || effective_uid || saved_uid || real_gid || effective_gid || saved_gid || getgroups(0,NULL)!=0 || getpid()!=1) fail("verify actual closed outer PID1 identity");
    check_map("/proc/self/uid_map",uid);check_map("/proc/self/gid_map",gid);
    if (prctl(PR_SET_DUMPABLE,0,0,0,0) || prctl(PR_GET_DUMPABLE)!=0 || prctl(PR_SET_NO_NEW_PRIVS,1,0,0,0) || prctl(PR_SET_PDEATHSIG,SIGKILL,0,0,0)) fail("protect mapped gvisor outer bootstrap");
    if (gate!=5 && dup3(gate,5,0)<0) fail("retain original outer parent-death gate");
    if (fcntl(3,F_SETFD,0) || fcntl(5,F_SETFD,0)) fail("fix closed outer control descriptor roles");
    /* Public code moves above the complete future vector before any low
     * target is reserved. This descriptor grants no private session right. */
    int helper=fcntl(4,F_DUPFD_CLOEXEC,(int)slots);
    if (helper<0 || close(4)) fail("retain fixed public helper above future descriptor slots");
    /* No pre-map socket ever receives later private rights. Both endpoints
     * are born only after the actual first-map/group/ND0 checks above. */
    int fresh[2];if (socketpair(AF_UNIX,SOCK_STREAM|SOCK_CLOEXEC,0,fresh)) fail("create post-map original outer control");
    int pass=1;
    if (setsockopt(fresh[0],SOL_SOCKET,SO_PASSCRED,&pass,sizeof(pass)) || setsockopt(fresh[1],SOL_SOCKET,SO_PASSCRED,&pass,sizeof(pass))) fail("require native post-map control senders");
    if (!native_frame(3,'N',0,fresh[0])) fail("transfer actual post-map control endpoint");
    close(fresh[0]);close(3);
    if (dup3(fresh[1],3,0)<0) fail("install sole post-map original control");
    close(fresh[1]);
    /* Keep every future target occupied before the Go runtime can allocate
     * its epoll/control files. Only the two fixed control roles differ from
     * /dev/null, and Go must refill their slots immediately after moving them. */
    int empty=open("/dev/null",O_RDWR|O_NOFOLLOW|O_CLOEXEC);struct stat empty_info;
    if (empty<0 || fstat(empty,&empty_info) || !S_ISCHR(empty_info.st_mode) || major(empty_info.st_rdev)!=1 || minor(empty_info.st_rdev)!=3) fail("capture fixed future descriptor placeholders");
    for (uint32_t target=0;target<slots;++target) {
        if (target==3 || target==5) continue;
        if ((int)target!=empty && dup3(empty,(int)target,0)<0) fail("reserve exact future Go descriptor slot");
        if ((int)target==empty && fcntl(empty,F_SETFD,0)) fail("inherit exact null descriptor placeholder");
    }
    if ((uint32_t)empty>=slots && close(empty)) fail("close high temporary null descriptor");
    struct sigaction ordinary_pipe={.sa_handler=SIG_DFL};sigemptyset(&ordinary_pipe.sa_mask);
    if (sigaction(SIGPIPE,&ordinary_pipe,NULL)) fail("restore fixed helper pipe signal semantics");
    /* exec resets dumpability. There are still no private rights to expose.
     * The fixed Go entry must set ND0 before receiving its original vector. */
    char *args[]={JAIL_ARGV0,"native-gvisor-init-v1","3",NULL};
    char *env[]={"PATH=/usr/bin:/bin","LANG=C","LC_ALL=C","TZ=UTC",NULL};
    syscall(SYS_execveat,helper,"",args,env,AT_EMPTY_PATH);
    fail("execute original fixed gvisor outer helper entry");
}
_Noreturn void gvisor_outer_v1(volatile sig_atomic_t *stopping) {
    uid_t uid=getuid();gid_t gid=getgid();pid_t creator=getppid();
    struct sigaction no_pipe={.sa_handler=SIG_IGN};sigemptyset(&no_pipe.sa_mask);
    if (sigaction(SIGPIPE,&no_pipe,NULL)) fail("retain owned join on a failed activation pipe");
    int type,domain;socklen_t size=sizeof(type);struct ucred peer;socklen_t peer_size=sizeof(peer);struct stat helper;
    if (getsockopt(3,SOL_SOCKET,SO_TYPE,&type,&size) || type!=SOCK_STREAM) fail("closed gvisor control is not its original stream");
    size=sizeof(domain);
    if (getsockopt(3,SOL_SOCKET,SO_DOMAIN,&domain,&size) || domain!=AF_UNIX || getsockopt(3,SOL_SOCKET,SO_PEERCRED,&peer,&peer_size) || peer_size!=sizeof(peer) || peer.pid!=creator || peer.uid!=uid || peer.gid!=gid) fail("closed gvisor control lost its original creator");
    int flags=fcntl(4,F_GETFL);
    if (flags<0 || (flags&O_ACCMODE)!=O_RDONLY || fstat(4,&helper) || !S_ISREG(helper.st_mode) || !(helper.st_mode&0111) || helper.st_mode&(S_ISUID|S_ISGID)) fail("closed gvisor bootstrap has no public fixed helper capability");
    int pass=1;if (setsockopt(3,SOL_SOCKET,SO_PASSCRED,&pass,sizeof(pass))) fail("require original outer activation sender");
    null_stdio();close_future_slots();
    int gate[2];if (pipe2(gate,O_CLOEXEC)) fail("make closed outer activation and death gate");
    int pidfd=-1;
    struct clone_args clone={.flags=CLONE_NEWUSER|CLONE_NEWPID|CLONE_NEWNS|CLONE_NEWNET|CLONE_NEWIPC|CLONE_NEWUTS|CLONE_NEWCGROUP|CLONE_PIDFD,.pidfd=(uint64_t)(uintptr_t)&pidfd,.exit_signal=SIGCHLD};
    pid_t child=(pid_t)syscall(SYS_clone3,&clone,sizeof(clone));
    if (child<0) {int saved=errno;(void)native_frame(3,'F',(pid_t)saved,-1);errno=saved;fail("create closed mapped gvisor outer PID1");}
    if (!child) {close(gate[1]);mapped_child(uid,gid,gate[0]);}
    close(gate[0]);
    if (!native_frame(3,'P',child,pidfd)) owned_failure(pidfd,child,"transfer original outer creation pidfd");
    uint32_t slots=0;
    if (!activation(3,creator,uid,gid,stopping,&slots)) owned_failure(pidfd,child,"original creator refused closed gvisor outer activation");
    /* C consumes the first5 bytes. The fixed Go entry consumes only the
     * versioned PUBLIC8-byte prelude on the SAME retained death gate. */
    unsigned char activate[13]={'G',(unsigned char)slots,(unsigned char)(slots>>8),(unsigned char)(slots>>16),(unsigned char)(slots>>24),'S','L','T',1,(unsigned char)slots,(unsigned char)(slots>>8),(unsigned char)(slots>>16),(unsigned char)(slots>>24)};
    if (write(gate[1],activate,sizeof(activate))!=(ssize_t)sizeof(activate)) owned_failure(pidfd,child,"release exact public outer descriptor reservation");
    close(3);close(4);
    int status;
    for (;;) {
        if (*stopping) (void)syscall(SYS_pidfd_send_signal,pidfd,SIGKILL,NULL,0);
        pid_t done=waitpid(child,&status,0);
        if (done==child) break;
        if (done<0 && errno==EINTR) continue;
        owned_failure(pidfd,child,"join original mapped gvisor outer PID1");
    }
    close(gate[1]);close(pidfd);
    _exit(WIFEXITED(status) ? WEXITSTATUS(status):WIFSIGNALED(status) ? 128+WTERMSIG(status):1);
}
