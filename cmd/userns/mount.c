/* SPDX-License-Identifier: Apache-2.0 */
#include "launcher.h"
#include <errno.h>
#include <fcntl.h>
#include <limits.h>
#include <linux/mount.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/mount.h>
#include <sys/stat.h>
#include <sys/socket.h>
#include <sys/sysmacros.h>
#include <sys/syscall.h>
#include <unistd.h>
#include "policy.h"

static bool beneath(const char *p,const char *base) {
    size_t n=strlen(base);return !strncmp(p,base,n) && (!p[n] || p[n]=='/');
}
static bool valid_path(const char *p) {
    if (!p || p[0]!='/' || !p[1] || strlen(p)>=PATH_MAX || p[strlen(p)-1]=='/') return false;
    for (const char *s=p;*s;++s) {
        if ((unsigned char)*s<32 || (unsigned char)*s==127) return false;
        if (*s=='/' && (s[1]=='/' || (s[1]=='.' && (!s[2] || s[2]=='/' || (s[2]=='.' && (!s[3] || s[3]=='/')))))) return false;
    }
    return true;
}
static int compare_guest(const void *a,const void *b) {
    const struct mount_request *left=a,*right=b;return strcmp(left->guest,right->guest);
}
static int compare_fd(const void *a,const void *b) {
    int left=*(const int *)a,right=*(const int *)b;return (left>right)-(left<right);
}
struct prefix_key {const char *path;size_t length;};
static int compare_prefix(const void *a,const void *b) {
    const struct prefix_key *key=a;const struct mount_request *mount=b;
    size_t n=strlen(mount->guest),common=key->length<n ? key->length:n;
    int compared=memcmp(key->path,mount->guest,common);
    if (compared) return compared;
    return (key->length>n)-(key->length<n);
}
void validate_sources(const struct request *r) {
    struct stat st;
    if (fstat(5,&st) || !S_ISDIR(st.st_mode) || st.st_uid!=getuid() || (st.st_mode&077)) fail("root descriptor is not this user's private directory");
    if (!r->probe) {
        int type;socklen_t n=sizeof(type);
        if (getsockopt(4,SOL_SOCKET,SO_TYPE,&type,&n) || type!=SOCK_STREAM) fail("link fd 4 is not a stream socket");
    }
    /* Sort once rather than scan every pair in a large valid request. */
    struct mount_request *sorted=r->mounts;
    if (r->count>1) qsort(sorted,r->count,sizeof(*sorted),compare_guest);
    int *fds=malloc((r->count ? r->count : 1)*sizeof(*fds));
    if (!fds) fail("allocate descriptor validation");
    for (size_t i=0;i<r->count;++i) {
        const struct mount_request *m=&r->mounts[i];
        if (!valid_path(m->guest)) fail("non-canonical mount destination");
        const char *hidden[]={"/proc","/dev","/run","/root","/sys","/etc/passwd","/etc/group","/etc/hosts","/etc/resolv.conf",NULL};
        for (size_t j=0;hidden[j];++j) if (beneath(m->guest,hidden[j])) fail("mount destination replaces a private system path");
        if (!strcmp(m->guest,"/etc") || !strcmp(m->guest,"/home") || !strcmp(m->guest,"/home/agent")) fail("mount replaces private configuration or home");
        const char *readonly[]={"/usr","/bin","/sbin","/lib","/lib64",HELPER_DIR,NULL};
        for (size_t j=0;m->write && readonly[j];++j) if (beneath(m->guest,readonly[j])) fail("writable system mount refused");
        if (i && !strcmp(m->guest,r->mounts[i-1].guest)) fail("duplicate launcher destination");
        for (const char *slash=m->guest+1;*slash;++slash) if (*slash=='/') {
            struct prefix_key key={m->guest,(size_t)(slash-m->guest)};
            if (bsearch(&key,r->mounts,r->count,sizeof(*r->mounts),compare_prefix)) fail("overlapping launcher mount");
        }
        fds[i]=m->fd;
        if (fstat(m->fd,&st) || !(S_ISREG(st.st_mode) || S_ISDIR(st.st_mode))) fail("mount source is not a pinned regular file or directory");
    }
    if (r->count>1) qsort(fds,r->count,sizeof(*fds),compare_fd);
    for (size_t i=1;i<r->count;++i) if (fds[i]==fds[i-1]) fail("duplicate launcher source descriptor");
    free(fds);
}
/* Walk only the fresh tmpfs. No target component is ever followed through a
 * symlink. Overlapping sources were refused before the first bind. */
static int target(int root,const char *path,bool directory) {
    char *copy=strdup(path+1),*save=NULL,*part;
    if (!copy) fail("allocate target path");
    int parent=fcntl(root,F_DUPFD_CLOEXEC,6);
    if (parent<0) fail("duplicate root descriptor");
    part=strtok_r(copy,"/",&save);
    while (part) {
        char *next=strtok_r(NULL,"/",&save);
        if (next || directory) {
            if (mkdirat(parent,part,0755) && errno!=EEXIST) fail("create mount target directory");
            int fd=openat(parent,part,O_PATH|O_DIRECTORY|O_NOFOLLOW|O_CLOEXEC);
            if (fd<0) fail("open mount target directory");
            close(parent);parent=fd;
        } else {
            int fd=openat(parent,part,O_CREAT|O_EXCL|O_WRONLY|O_NOFOLLOW|O_CLOEXEC,0644);
            if (fd<0) fail("create mount target file");
            close(parent);parent=fd;
        }
        part=next;
    }
    free(copy);return parent;
}
static void private_mount(const char *source,int destination,const char *type,unsigned long flags,const char *data) {
    char to[64];snprintf(to,sizeof(to),"/proc/self/fd/%d",destination);
    if (mount(source,to,type,flags,data)) fail("mount private sandbox filesystem");
}
static void bind_source(int root,int source,const char *guest,bool write,bool device) {
    struct stat st;
    if (fstat(source,&st)) fail("stat pinned bind source");
    int to=target(root,guest,S_ISDIR(st.st_mode));
    char from[64];snprintf(from,sizeof(from),"/proc/self/fd/%d",source);
    private_mount(from,to,NULL,MS_BIND|MS_REC,NULL);
    /* The descriptor used as the target names the old mount. Resolve the
     * destination once more from our private tmpfs after the bind, then pin
     * the mounted tree. No guest is running and targets contain no symlinks. */
    int mounted=openat(root,guest+1,O_PATH|O_NOFOLLOW|O_CLOEXEC);
    if (mounted<0) fail("pin mounted tree");
    struct mount_attr attr={.attr_set=MOUNT_ATTR_NOSUID|(device ? 0 : MOUNT_ATTR_NODEV)|(write ? 0 : MOUNT_ATTR_RDONLY)};
    if (syscall(SYS_mount_setattr,mounted,"",AT_EMPTY_PATH|AT_RECURSIVE,&attr,sizeof(attr))) fail("restrict whole bind tree");
    close(mounted);close(to);
}
static void file(int root,const char *path,const char *text) {
    int fd=target(root,path,false);size_t n=strlen(text),done=0;
    while (done<n) {ssize_t got=write(fd,text+done,n-done);if (got<0 && errno==EINTR) continue;if (got<=0) fail("write fixed guest configuration");done+=(size_t)got;}
    close(fd);
}
static void directory(int root,const char *path,mode_t mode) {
    int fd=target(root,path,true);
    /* fd is O_PATH. chmod through a private in-root path cannot reach a
     * host source: all of these directories precede the binds. */
    if (fchmodat(root,path+1,mode,0)) fail("protect private directory");
    close(fd);
}
int build_root(const struct request *r) {
    if (mount(NULL,"/",NULL,MS_REC|MS_PRIVATE,NULL)) fail("make mount namespace private");
    int fs=(int)syscall(SYS_fsopen,"tmpfs",FSOPEN_CLOEXEC);
    if (fs<0 || syscall(SYS_fsconfig,fs,FSCONFIG_CMD_CREATE,NULL,NULL,0)) fail("create detached tmpfs root");
    int root=(int)syscall(SYS_fsmount,fs,FSMOUNT_CLOEXEC,MOUNT_ATTR_NOSUID|MOUNT_ATTR_NODEV);
    close(fs);
    if (root<0 || syscall(SYS_move_mount,root,"",5,"",MOVE_MOUNT_F_EMPTY_PATH|MOVE_MOUNT_T_EMPTY_PATH)) fail("attach private tmpfs root");
    const char *dirs[]={"/home/agent","/tmp","/var/tmp","/etc","/proc","/dev","/dev/pts","/dev/shm",GUEST_RUN,"/work","/mnt","/.old",NULL};
    for (size_t i=0;dirs[i];++i) directory(root,dirs[i],0755);
    directory(root,"/home/agent",0700);directory(root,"/tmp",01777);directory(root,"/var/tmp",01777);directory(root,GUEST_RUN,0700);
    int dev=target(root,"/dev",true);private_mount("tmpfs",dev,"tmpfs",MS_NOSUID|MS_NOEXEC,"mode=0755");close(dev);
    directory(root,"/dev/pts",0755);directory(root,"/dev/shm",01777);
    int pts=target(root,"/dev/pts",true);private_mount("devpts",pts,"devpts",MS_NOSUID|MS_NOEXEC,"newinstance,ptmxmode=0666,mode=0620");close(pts);
    int shm=target(root,"/dev/shm",true);private_mount("tmpfs",shm,"tmpfs",MS_NOSUID|MS_NODEV,"mode=1777");close(shm);
    const char *devices[]={"null","zero","full","random","urandom","tty",NULL};
    const unsigned minors[]={3,5,7,8,9,0};
    for (size_t i=0;devices[i];++i) {
        char path[32];snprintf(path,sizeof(path),"/dev/%s",devices[i]);
        int source=open(path,O_PATH|O_NOFOLLOW|O_CLOEXEC);struct stat st;
        if (source<0 || fstat(source,&st) || !S_ISCHR(st.st_mode) || major(st.st_rdev)!=(i==5 ? 5U : 1U) || minor(st.st_rdev)!=minors[i]) fail("invalid fixed device source");
        bind_source(root,source,path,true,true);close(source);
    }
    if (symlinkat("pts/ptmx",root,"dev/ptmx") || symlinkat("/proc/self/fd",root,"dev/fd") || symlinkat("/proc/self/fd/0",root,"dev/stdin") || symlinkat("/proc/self/fd/1",root,"dev/stdout") || symlinkat("/proc/self/fd/2",root,"dev/stderr")) fail("make private device links");
    int proc=target(root,"/proc",true);private_mount("proc",proc,"proc",MS_NOSUID|MS_NODEV|MS_NOEXEC,NULL);close(proc);
    for (size_t i=0;i<r->count;++i) bind_source(root,r->mounts[i].fd,r->mounts[i].guest,r->mounts[i].write,false);
    file(root,"/etc/passwd","agent:x:1000:1000:agent:/home/agent:/bin/sh\n");
    file(root,"/etc/group","agent:x:1000:\n");
    file(root,"/etc/hosts","127.0.0.1 localhost\n::1 localhost\n");
    file(root,"/etc/resolv.conf","nameserver 127.0.0.53\n");
    return root;
}
void enter_root(int root) {
    if (fchdir(root) || syscall(SYS_pivot_root,".",".old") || chdir("/") || umount2("/.old",MNT_DETACH) || rmdir("/.old")) fail("pivot into private sandbox root");
    close(root);close(5);
}
