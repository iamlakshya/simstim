/* SPDX-License-Identifier: Apache-2.0 */
#include "launcher.h"
#include <errno.h>
#include <fcntl.h>
#include <linux/capability.h>
#include <linux/filter.h>
#include <linux/seccomp.h>
#include <net/if.h>
#include <netinet/in.h>
#include <sched.h>
#include <stdio.h>
#include <string.h>
#include <sys/ioctl.h>
#include <sys/prctl.h>
#include <sys/socket.h>
#include <sys/stat.h>
#include <sys/syscall.h>
#include <unistd.h>
#define PROCESS_FILTER
#include "policy.h"

static void write_map(const char *path,const char *value) {
    int fd=open(path,O_WRONLY|O_CLOEXEC|O_NOFOLLOW);
    size_t n=strlen(value);
    if (fd<0 || write(fd,value,n)!=(ssize_t)n || close(fd)) fail("write namespace ID map");
}
void bring_up_loopback(void) {
    int fd=socket(AF_INET,SOCK_DGRAM|SOCK_CLOEXEC,0);
    struct ifreq lo={0};strcpy(lo.ifr_name,"lo");
    if (fd<0 || ioctl(fd,SIOCGIFFLAGS,&lo)) fail("read private loopback flags");
    lo.ifr_flags|=IFF_UP;
    if (ioctl(fd,SIOCSIFFLAGS,&lo)) fail("bring up private loopback");
    close(fd);
}
void enter_agent_user(void) {
    if (unshare(CLONE_NEWUSER)) fail("make child agent user namespace");
    write_map("/proc/self/uid_map","1000 0 1\n");
    write_map("/proc/self/setgroups","deny\n");
    write_map("/proc/self/gid_map","1000 0 1\n");
    if (setresgid(1000,1000,1000) || setresuid(1000,1000,1000)) fail("become sandbox agent");
    if (getuid()!=1000 || getgid()!=1000) fail("agent namespace mapping is incomplete");
}
/* Use the kernel's versioned structure layout directly. This keeps the
 * launcher build independent of the build machine's Landlock header age. */
struct ruleset_attr { uint64_t fs,net,scoped; };
struct path_attr { uint64_t access; int32_t fd; } __attribute__((packed));
static uint64_t fs_rights(unsigned abi) {
    if (abi==1) return (1ULL<<13)-1;
    if (abi==2) return (1ULL<<14)-1;
    if (abi<5) return (1ULL<<15)-1;
    if (abi<9) return (1ULL<<16)-1;
    return (1ULL<<17)-1;
}
static void rule(int ruleset,unsigned abi,const char *path,bool write) {
    int fd=open(path,O_PATH|O_CLOEXEC|O_NOFOLLOW);
    struct stat st;
    if (fd<0 || fstat(fd,&st)) fail("open Landlock path");
    uint64_t access=(1ULL<<0)|(1ULL<<2)|(1ULL<<3); /* execute, read file, read dir */
    if (write) access|=(1ULL<<1)|(1ULL<<4)|(1ULL<<5)|(1ULL<<7)|(1ULL<<8)|(1ULL<<9)|(1ULL<<10)|(1ULL<<12)|(1ULL<<13)|(1ULL<<14)|(1ULL<<15)|(1ULL<<16);
    if (!S_ISDIR(st.st_mode)) access&=(1ULL<<0)|(1ULL<<1)|(1ULL<<2)|(1ULL<<14)|(1ULL<<15)|(1ULL<<16);
    struct path_attr attr={.access=access&fs_rights(abi),.fd=fd};
    if (syscall(SYS_landlock_add_rule,ruleset,1,&attr,0)) fail("add mandatory Landlock path rule");
    close(fd);
}
static void landlock(const struct request *r) {
    long abi=syscall(SYS_landlock_create_ruleset,NULL,0,1);
    if (abi<1) fail("Landlock ABI 1 or later is required");
    struct ruleset_attr attr={.fs=fs_rights((unsigned)abi),.scoped=abi>=6 ? 3 : 0};
    size_t size=abi>=6 ? sizeof(attr) : sizeof(attr.fs);
    int rs=(int)syscall(SYS_landlock_create_ruleset,&attr,size,0);
    if (rs<0) fail("create mandatory Landlock ruleset");
    rule(rs,(unsigned)abi,"/",false);
    const char *writable[]={"/home/agent","/work","/tmp","/var/tmp",GUEST_RUN,"/dev",NULL};
    for (size_t i=0;writable[i];++i) rule(rs,(unsigned)abi,writable[i],true);
    for (size_t i=0;i<r->count;++i) if (r->mounts[i].write) rule(rs,(unsigned)abi,r->mounts[i].guest,true);
    if (syscall(SYS_landlock_restrict_self,rs,0)) fail("enforce mandatory Landlock ruleset");
    close(rs);
}
void confine(const struct request *r) {
    if (prctl(PR_SET_NO_NEW_PRIVS,1,0,0,0) || prctl(PR_SET_DUMPABLE,0,0,0,0)) fail("set no_new_privs and clear dumpability");
    for (int cap=0;cap<64;++cap) {
        if (prctl(PR_CAPBSET_DROP,cap,0,0,0) && errno!=EINVAL) fail("drop capability bounding set");
    }
    if (prctl(PR_CAP_AMBIENT,PR_CAP_AMBIENT_CLEAR_ALL,0,0,0) && errno!=EINVAL) fail("clear ambient capabilities");
    struct __user_cap_header_struct hdr={.version=_LINUX_CAPABILITY_VERSION_3};
    struct __user_cap_data_struct data[2]={{0}};
    if (syscall(SYS_capset,&hdr,data)) fail("clear effective permitted and inheritable capabilities");
    landlock(r);
    struct sock_fprog prog={.len=(unsigned short)(sizeof(process_filter)/sizeof(process_filter[0])),.filter=(struct sock_filter *)process_filter};
    if (syscall(SYS_seccomp,SECCOMP_SET_MODE_FILTER,0,&prog)) fail("load mandatory seccomp filter");
}
