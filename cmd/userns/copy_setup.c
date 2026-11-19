/* SPDX-License-Identifier: Apache-2.0 */
#include "copy.h"
#include <errno.h>
#include <fcntl.h>
#include <poll.h>
#include <signal.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/mount.h>
#include <sys/random.h>
#include <sys/socket.h>
#include <unistd.h>
#define COPY_ROOT UINT32_MAX
#define COPY_END (UINT32_MAX-1)
static void send_cap(int channel,uint32_t index,int a,int b) {
    int fds[2]={a,b};size_t count=a<0 ? 0 : b<0 ? 1 : 2;
    struct iovec io={.iov_base=&index,.iov_len=sizeof(index)};
    union {struct cmsghdr align;char bytes[CMSG_SPACE(sizeof(fds))];} control={0};
    struct msghdr message={.msg_iov=&io,.msg_iovlen=1};
    if (count) {
        message.msg_control=control.bytes;message.msg_controllen=CMSG_SPACE(count*sizeof(int));
        struct cmsghdr *c=CMSG_FIRSTHDR(&message);c->cmsg_level=SOL_SOCKET;c->cmsg_type=SCM_RIGHTS;c->cmsg_len=CMSG_LEN(count*sizeof(int));memcpy(CMSG_DATA(c),fds,count*sizeof(int));
    }
    if (sendmsg(channel,&message,MSG_NOSIGNAL)!=(ssize_t)sizeof(index)) fail("transfer original copy resource");
}
static void mask_stage(int target) {
    if (target<0) fail("capture private copy mask destination");
    char locator[64];snprintf(locator,sizeof(locator),"/proc/self/fd/%d",target);
    if (mount("tmpfs",locator,"tmpfs",MS_RDONLY|MS_NOSUID|MS_NODEV|MS_NOEXEC,"mode=000")) fail("mask original copy staging from guest");
}

void copy_prepare(int root,const struct request *r,int channel) {
    if (mkdirat(root,".copy-staging",0700)) fail("make private fresh-root copy staging");
    int stage=openat(root,".copy-staging",O_PATH|O_DIRECTORY|O_NOFOLLOW|O_CLOEXEC);
    if (stage<0) fail("pin private fresh-root staging");
    mask_stage(stage);
    send_cap(channel,COPY_ROOT,root,stage);close(stage);
    for (size_t i=0;i<r->count;++i) {
        const struct mount_request *mount=&r->mounts[i];struct stat info;
        if (!mount->write) continue;
        if (fstat(mount->fd,&info)) fail("identify original writable copy mount");
        if (!S_ISDIR(info.st_mode)) continue; /* A file bind cannot atomically replace its mount point. */
        if (!mount->stage_fd) continue; /* No same-mount private producer. */
        const char *name=mount->stage_name;
        stage=fcntl(mount->stage_fd,F_DUPFD_CLOEXEC,7);
        if (stage<0) fail("pin original factory-created staging");
        /* The peer is the still-trusted outer process, created before any
         * guest code. The name belongs to this exact retained directory. */
        send_cap(channel,(uint32_t)i,stage,-1);
        if (send(channel,name,39,MSG_NOSIGNAL)!=39) fail("transfer owned staging name");
        close(stage);
    }
    send_cap(channel,COPY_END,-1,-1);
    char ack;if (read(channel,&ack,1)!=1 || ack!='A') fail("outer owner did not retain original copy resources");
}
static uint32_t receive_cap(int channel,int init_pidfd,int fds[2],size_t *count) {
    struct pollfd pending[2]={{.fd=channel,.events=POLLIN},{.fd=init_pidfd,.events=POLLIN}};
    int ready;do {ready=poll(pending,2,30000);} while (ready<0 && errno==EINTR);
    if (ready<=0 || !(pending[0].revents&POLLIN)) fail("trusted initializer ended before copy resource transfer");
    uint32_t index=0;struct iovec io={.iov_base=&index,.iov_len=sizeof(index)};
    union {struct cmsghdr align;char bytes[CMSG_SPACE(2*sizeof(int))];} control={0};
    struct msghdr message={.msg_iov=&io,.msg_iovlen=1,.msg_control=control.bytes,.msg_controllen=sizeof(control.bytes)};
    ssize_t n=recvmsg(channel,&message,MSG_CMSG_CLOEXEC);*count=0;
    if (n!=(ssize_t)sizeof(index) || message.msg_flags&(MSG_TRUNC|MSG_CTRUNC)) fail("invalid original copy resource frame");
    size_t cursor=0;struct cmsghdr *c;
    while ((c=native_control_next(&message,&cursor))!=NULL) {
        if (c->cmsg_level!=SOL_SOCKET || c->cmsg_type!=SCM_RIGHTS || c->cmsg_len<CMSG_LEN(0)) fail("unexpected original copy resource control");
        size_t bytes=c->cmsg_len-CMSG_LEN(0),number=bytes/sizeof(int);
        if (bytes%sizeof(int) || *count+number>2) fail("too many original copy descriptors");
        memcpy(fds+*count,CMSG_DATA(c),bytes);*count+=number;
    }
    return index;
}
void copy_capture(struct copy_owner *owner,const struct request *r,int channel,int init_pidfd,int proc) {
    owner->request=r;owner->proc=proc;owner->root=-1;
    owner->mounts=calloc(r->count+1,sizeof(*owner->mounts));if (!owner->mounts) fail("allocate original copy resources");
    bool *seen=calloc(r->count+1,sizeof(bool));if (!seen) fail("allocate original copy resource identities");
    for (;;) {
        int fds[2]={-1,-1};size_t count=0;uint32_t index=receive_cap(channel,init_pidfd,fds,&count);
        if (index==COPY_END) {if (count || owner->root<0) fail("incomplete original copy resources");break;}
        if (index!=COPY_ROOT && index>=r->count) fail("unknown original copy mount");
        size_t slot=index==COPY_ROOT ? r->count:index;
        if (seen[slot] || count!=(index==COPY_ROOT ? 2U:1U)) fail("duplicate or incomplete original copy mount");seen[slot]=true;
        struct copy_mount *mount=&owner->mounts[owner->count++];mount->index=index;
        if (index==COPY_ROOT) {owner->root=fds[0];mount->root=fds[0];mount->stage=fds[1];mount->prefix="/";}
        else {
            if (!r->mounts[index].write) fail("copy resource names a read-only mount");
            mount->root=r->mounts[index].fd;mount->stage=fds[0];mount->prefix=r->mounts[index].guest;
            mount->cleanup_root=openat(mount->stage,"..",O_PATH|O_DIRECTORY|O_NOFOLLOW|O_CLOEXEC);if (mount->cleanup_root<0) fail("capture original staging cleanup parent");
            char name[39];if (recv(channel,name,sizeof(name),MSG_WAITALL)!=(ssize_t)sizeof(name) || memcmp(name,".copy-",6) || name[38]) fail("invalid owned staging name");
            for (size_t i=6;i<38;++i) if (!((name[i]>='0' && name[i]<='9') || (name[i]>='a' && name[i]<='f'))) fail("invalid staging identity");
            mount->owned_name=strdup(name);if (!mount->owned_name) fail("retain staging identity");
        }
        if (fstat(mount->stage,&mount->identity) || !S_ISDIR(mount->identity.st_mode) || mount->identity.st_uid!=getuid() || (mount->identity.st_mode&077)) fail("invalid factory-owned private stage");
    }
    for (size_t i=0;i<r->count;++i) {
        struct stat info;if (fstat(r->mounts[i].fd,&info)) fail("inspect retained original mount");
        if (r->mounts[i].stage_fd && !seen[i]) fail("missing original writable mount resource");
    }
    free(seen);
    if (write(channel,"A",1)!=1) fail("acknowledge original child copy resources");
    for (size_t i=0;i<owner->count;++i) {
        struct copy_mount *mount=&owner->mounts[i];send_cap(6,mount->index,mount->root,mount->stage);
        if (mount->owned_name && send(6,mount->owned_name,39,MSG_NOSIGNAL)!=39) fail("transfer exact factory cleanup identity");
    }
    send_cap(6,COPY_END,-1,-1);
}
void copy_release(struct copy_owner *owner) {
    for (size_t i=0;i<owner->count;++i) {
        struct copy_mount *mount=&owner->mounts[i];
        if (mount->owned_name) {
            struct stat current;
            if (fstatat(mount->cleanup_root,mount->owned_name,&current,AT_SYMLINK_NOFOLLOW)==0 && current.st_dev==mount->identity.st_dev && current.st_ino==mount->identity.st_ino) {
                if (unlinkat(mount->cleanup_root,mount->owned_name,AT_REMOVEDIR)) fail("release exact factory-owned staging directory");
            } else fail("owned staging directory identity changed");
            free(mount->owned_name);close(mount->cleanup_root);
        }
        close(mount->stage);
    }
    if (owner->root>=0) close(owner->root);free(owner->mounts);
}
