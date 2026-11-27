/* SPDX-License-Identifier: Apache-2.0 */
#include "copy.h"
#include <errno.h>
#include <fcntl.h>
#include <grp.h>
#include <linux/capability.h>
#include <linux/magic.h>
#include <linux/openat2.h>
#include <linux/fs.h>
#include <poll.h>
#include <sched.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/prctl.h>
#include <sys/syscall.h>
#include <sys/vfs.h>
#include <unistd.h>
static int directory(int root,const char *relative) {
    char *copy=strdup(relative),*save=NULL;if (!copy) return -1;
    int parent=fcntl(root,F_DUPFD_CLOEXEC,7);if (parent<0) {free(copy);return -1;}
    for (char *part=strtok_r(copy,"/",&save);part;part=strtok_r(NULL,"/",&save)) {
        struct open_how how={.flags=O_PATH|O_DIRECTORY|O_NOFOLLOW|O_CLOEXEC,.resolve=RESOLVE_BENEATH|RESOLVE_NO_SYMLINKS|RESOLVE_NO_MAGICLINKS};
        int next=(int)syscall(SYS_openat2,parent,part,&how,sizeof(how));
        if (next<0 && errno==ENOENT) {
            if (mkdirat(parent,part,0755) && errno!=EEXIST) {int e=errno;close(parent);free(copy);errno=e;return -1;}
            next=(int)syscall(SYS_openat2,parent,part,&how,sizeof(how));
        }
        if (next<0) {int e=errno;close(parent);free(copy);errno=e;return -1;}
        close(parent);parent=next;
    }
    free(copy);return parent;
}
static int untraced(int proc) {
    int fd=openat(proc,"self/status",O_RDONLY|O_CLOEXEC);struct statfs fs;
    if (fd<0 || fstatfs(fd,&fs) || fs.f_type!=PROC_SUPER_MAGIC) {if (fd>=0) close(fd);return EPERM;}
    FILE *status=fdopen(fd,"r");if (!status) {close(fd);return errno;}
    char *line=NULL;size_t capacity=0,total=0;int result=EPERM;
    for (;;) {
        ssize_t n=getline(&line,&capacity,status);if (n<0) break;total+=(size_t)n;if (total>65536) break;
        if (!strncmp(line,"TracerPid:",10)) {
            char *end;errno=0;unsigned long tracer=strtoul(line+10,&end,10);
            if (!errno && end!=line+10 && !tracer) result=0;break;
        }
    }
    free(line);fclose(status);return result;
}
static int fixed_account(struct copy_owner *owner,int parent_gate) {
    if (prctl(PR_SET_PDEATHSIG,SIGKILL,0,0,0)) return errno;
    if (unshare(CLONE_NEWUSER)) return errno;
    const char *paths[]={"self/uid_map","self/setgroups","self/gid_map"};
    const char *values[]={"1000 0 1\n","deny\n","1000 0 1\n"};
    for (size_t i=0;i<3;++i) {
        int fd=openat(owner->proc,paths[i],O_WRONLY|O_CLOEXEC|O_NOFOLLOW);size_t size=strlen(values[i]);
        if (fd<0) return errno;
        if (write(fd,values[i],size)!=(ssize_t)size) {int e=errno;close(fd);return e ? e:EIO;}
        if (close(fd)) return errno;
    }
    if (setresgid(1000,1000,1000) || setresuid(1000,1000,1000)) return errno;
    if (prctl(PR_SET_DUMPABLE,0,0,0,0) || prctl(PR_SET_NO_NEW_PRIVS,1,0,0,0) || prctl(PR_SET_PDEATHSIG,SIGKILL,0,0,0)) return errno;
    struct pollfd parent={.fd=parent_gate,.events=POLLIN};if (poll(&parent,1,0)!=0) return ECANCELED;
    if (getuid()!=1000 || geteuid()!=1000 || getgid()!=1000 || getegid()!=1000 || getgroups(0,NULL)!=0 || prctl(PR_GET_DUMPABLE)!=0) return EPERM;
    int traced=untraced(owner->proc);if (traced) return traced;
    for (int cap=0;cap<64;++cap) if (prctl(PR_CAPBSET_DROP,cap,0,0,0) && errno!=EINVAL) return errno;
    if (prctl(PR_CAP_AMBIENT,PR_CAP_AMBIENT_CLEAR_ALL,0,0,0) && errno!=EINVAL) return errno;
    struct __user_cap_header_struct header={.version=_LINUX_CAPABILITY_VERSION_3};
    struct __user_cap_data_struct empty[2]={{0}};
    if (syscall(SYS_capset,&header,empty)) return errno;
    return 0;
}
int copy_publish(struct copy_owner *owner,struct copy_transaction *tx,int parent_gate) {
    char token;
    if (prctl(PR_SET_PDEATHSIG,SIGKILL,0,0,0) || read(parent_gate,&token,1)!=1 || token!='A') return ECANCELED;
    int result=fixed_account(owner,parent_gate);if (result) return result;
    /* Every destination lookup happens after the actual fixed-account drop.
     * The visible walk checks all guest ancestors; the second walk uses only
     * the original factory mount, so staging and rename share that mount. */
    char target[PATH_MAX];memcpy(target,tx->target,strlen(tx->target)+1);
    char *slash=strrchr(target,'/');if (!slash || !slash[1]) return EINVAL;
    const char *leaf=slash+1;*slash=0;
    int visible=directory(owner->root,target+1);if (visible<0) return errno;
    const char *relative=target+strlen(tx->mount->prefix);
    if (tx->mount->prefix[1]==0) relative=target+1;
    else if (*relative=='/') ++relative;
    int original=directory(tx->mount->root,relative);
    if (original<0) {result=errno;close(visible);return result;}
    struct stat a,b,source,current;
    if (fstat(visible,&a) || fstat(original,&b) || a.st_dev!=b.st_dev || a.st_ino!=b.st_ino) {result=EXDEV;goto done;}
    if (fstat(tx->file,&source) || fstatat(tx->mount->stage,tx->name,&current,AT_SYMLINK_NOFOLLOW)) {result=errno;goto done;}
    if (!S_ISREG(source.st_mode) || source.st_uid!=1000 || (source.st_mode&07777)!=0644 || source.st_nlink!=1 || source.st_dev!=tx->identity.st_dev || source.st_ino!=tx->identity.st_ino || source.st_dev!=current.st_dev || source.st_ino!=current.st_ino) {result=ESTALE;goto done;}
    if (!fstatat(original,leaf,&current,AT_SYMLINK_NOFOLLOW)) {
        if (!tx->replace || S_ISDIR(current.st_mode)) {result=EEXIST;goto done;}
    } else if (errno!=ENOENT) {result=errno;goto done;}
    if (syscall(SYS_renameat2,tx->mount->stage,tx->name,original,leaf,tx->replace ? 0:RENAME_NOREPLACE)) {result=errno;goto done;}
    /* O_PATH preserves write/search-only access during lookup. Reopen this
     * exact directory for durable commit; failure after rename is uncertain. */
    int durable=openat(original,".",O_RDONLY|O_DIRECTORY|O_CLOEXEC);
    if (durable<0) {result=errno;goto done;}
    if (fsync(durable)) result=errno;
    if (close(durable) && !result) result=errno;
done:
    close(original);close(visible);return result;
}
