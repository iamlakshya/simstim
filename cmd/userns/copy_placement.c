/* SPDX-License-Identifier: Apache-2.0 */
#include "copy.h"
#include <errno.h>
#include <fcntl.h>
/* Use the kernel's current statx ABI without redefining musl's older tags. */
#define statx native_kernel_statx
#define statx_timestamp native_kernel_statx_timestamp
#include <linux/stat.h>
#undef statx_timestamp
#undef statx
#include <sys/syscall.h>
#include <unistd.h>
bool copy_same_mount(int a,int b) {
    struct native_kernel_statx left={0},right={0};
    if (syscall(SYS_statx,a,"",AT_EMPTY_PATH|AT_NO_AUTOMOUNT,STATX_MNT_ID,&left) || syscall(SYS_statx,b,"",AT_EMPTY_PATH|AT_NO_AUTOMOUNT,STATX_MNT_ID,&right) || !(left.stx_mask&STATX_MNT_ID) || !(right.stx_mask&STATX_MNT_ID)) fail("capture exact original copy mount identity");
    return left.stx_mnt_id==right.stx_mnt_id;
}
bool copy_contains(int source,int descendant) {
    struct stat wanted,current,parent;
    if (fstat(source,&wanted)) fail("inspect original guest source");
    if (!S_ISDIR(wanted.st_mode)) return false;
    int fd=openat(descendant,".",O_PATH|O_DIRECTORY|O_NOFOLLOW|O_CLOEXEC);
    if (fd<0) fail("walk private copy staging ancestry");
    for (;;) {
        if (fstat(fd,&current)) fail("inspect captured staging ancestor");
        if (wanted.st_dev==current.st_dev && wanted.st_ino==current.st_ino) {close(fd);return true;}
        int next=openat(fd,"..",O_PATH|O_DIRECTORY|O_NOFOLLOW|O_CLOEXEC);
        if (next<0 || fstat(next,&parent)) fail("open captured staging parent");
        close(fd);
        if (current.st_dev==parent.st_dev && current.st_ino==parent.st_ino) {close(next);return false;}
        fd=next;
    }
}
