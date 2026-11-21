/* SPDX-License-Identifier: Apache-2.0
 * The only executable transition is the fixed guest init after confinement. */
#include "launcher.h"
#include "copy.h"
#include <linux/sched.h>
#include <errno.h>
#include <fcntl.h>
#include <grp.h>
#include <linux/filter.h>
#include <linux/capability.h>
#include <linux/magic.h>
#include <sys/vfs.h>
#include <netinet/in.h>
#include <poll.h>
#include <sched.h>
#include <signal.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/prctl.h>
#include <sys/resource.h>
#include <sys/socket.h>
#include <sys/syscall.h>
#include <sys/wait.h>
#include <unistd.h>
#include "policy.h"

static volatile sig_atomic_t stopping;
static void interrupted(int sig) { (void)sig;stopping=1; }
_Noreturn void fail(const char *what) {
    int saved=errno;
    dprintf(2,"Error: namespace launcher: %s (%d).\nTry: run host prepare, then read the supervisor log.\n",what,saved);
    _exit(1);
}
static void map(const char *path,unsigned inside,unsigned outside) {
    char text[80];int n=snprintf(text,sizeof(text),"%u %u 1\n",inside,outside);
    int fd=open(path,O_WRONLY|O_NOFOLLOW|O_CLOEXEC);
    if (fd<0 || write(fd,text,(size_t)n)!=n || close(fd)) fail("write outer namespace ID map");
}
static _Noreturn void handoff_failed(int pidfd,pid_t pid,const char *why) {
    syscall(SYS_pidfd_send_signal,pidfd,SIGKILL,NULL,0);
    while (waitpid(pid,NULL,0)<0 && errno==EINTR) {}
    fail(why);
}
static void wait_ack(int pidfd,pid_t pid) {
    char byte='P';struct iovec iov={.iov_base=&byte,.iov_len=1};
    union {struct cmsghdr align;char data[CMSG_SPACE(sizeof(int))];} control={0};
    struct msghdr msg={.msg_iov=&iov,.msg_iovlen=1,.msg_control=control.data,.msg_controllen=sizeof(control.data)};
    struct cmsghdr *c=CMSG_FIRSTHDR(&msg);c->cmsg_level=SOL_SOCKET;c->cmsg_type=SCM_RIGHTS;c->cmsg_len=CMSG_LEN(sizeof(int));memcpy(CMSG_DATA(c),&pidfd,sizeof(pidfd));
    if (sendmsg(4,&msg,MSG_NOSIGNAL)!=1) handoff_failed(pidfd,pid,"transfer namespace-init pidfd");
    struct pollfd p={.fd=4,.events=POLLIN};
    int ready;
    do {ready=poll(&p,1,30000);} while (ready<0 && errno==EINTR && !stopping);
    if (stopping || ready!=1 || read(4,&byte,1)!=1 || byte!='A') {
        handoff_failed(pidfd,pid,"supervisor did not acknowledge ownership of namespace init");
    }
}
static int dns_socket(int type) {
    int fd=socket(AF_INET,type|SOCK_CLOEXEC,0);
    struct sockaddr_in addr={.sin_family=AF_INET,.sin_port=htons(53),.sin_addr={.s_addr=htonl(0x7f000035)}};
    if (fd<0 || bind(fd,(struct sockaddr *)&addr,sizeof(addr)) || (type==SOCK_STREAM && listen(fd,128))) fail("bind private DNS endpoint");
    return fd;
}
static void descriptor(int from,int to) {
    if (from!=to && dup3(from,to,0)<0) fail("remap guest descriptor");
    if (fcntl(to,F_SETFD,0)) fail("make guest descriptor inheritable");
}
void close_setup_fds(void) {
    if (!syscall(SYS_close_range,6U,~0U,0)) return;
    if (errno!=ENOSYS && errno!=EINVAL) fail("close setup descriptors");
    struct rlimit limit;if (getrlimit(RLIMIT_NOFILE,&limit)) fail("read descriptor limit");
    for (rlim_t fd=6;fd<limit.rlim_cur;++fd) close((int)fd);
}
static void child(struct request *r,int gate,int copy_channel) {
    if (prctl(PR_SET_PDEATHSIG,SIGKILL,0,0,0)) fail("arm namespace-init parent death");
    char token;
    if (read(gate,&token,1)!=1 || token!='G') fail("launcher ended before namespace activation");
    if (copy_channel>=0) close(6); /* Only the outer owner receives copy commands. */
    int root=build_root(r,copy_channel);
    if (copy_channel>=0) close(copy_channel);
    bring_up_loopback();
    int udp=-1,tcp=-1;
    if (r->dns) {udp=dns_socket(SOCK_DGRAM);tcp=dns_socket(SOCK_STREAM);}
    enter_root(root);
    enter_agent_user();
    /* Becoming the mapped uid clears PDEATHSIG. Re-arm it, then prove the
     * parent still holds its sole writer. No child inherits that writer. */
    if (prctl(PR_SET_PDEATHSIG,SIGKILL,0,0,0)) fail("re-arm namespace-init parent death");
    struct pollfd parent={.fd=gate,.events=POLLIN};
    if (poll(&parent,1,0)!=0) fail("launcher ended during agent mapping");
    close(gate);
    if (!r->probe) descriptor(4,3);
    if (r->dns) {descriptor(udp,4);descriptor(tcp,5);} else {close(4);close(5);}
    close_setup_fds();
    confine(r);
    if (r->probe) {
        const char result[]="{\"userns\":true,\"seccomp\":true}\n";
        if (write(1,result,sizeof(result)-1)!=(ssize_t)(sizeof(result)-1)) fail("write probe result");
        _exit(0);
    }
    char *argv[]={HELPER_PATH,"wall-init",NULL};
    char dns[128];snprintf(dns,sizeof(dns),"%s=4,5",DNS_MARKER);
    char *env[]={"PATH=/usr/local/bin:/usr/bin:/bin","HOME=/home/agent","USER=agent","LOGNAME=agent","LANG=C.UTF-8",NULL,NULL};
    if (r->dns) env[6]=dns;
    execve(HELPER_PATH,argv,env);
    fail("execute fixed guest init");
}
static void refuse_tracer(void) {
    FILE *status=fopen("/proc/self/status","re");struct statfs fs;
    if (!status || fstatfs(fileno(status),&fs) || fs.f_type!=PROC_SUPER_MAGIC) fail("require genuine procfs status");
    char *line=NULL;size_t capacity=0,total=0;bool found=false;
    for (;;) {
        ssize_t n=getline(&line,&capacity,status);
        if (n<0) break;total+=(size_t)n;
        if (total>REQUEST_LIMIT) fail("proc status exceeds its bound");
        if (!strncmp(line,"TracerPid:",10)) {
            char *end;errno=0;unsigned long tracer=strtoul(line+10,&end,10);
            if (errno || end==line+10 || tracer) fail("an already traced launcher cannot make namespaces");
            found=true;break;
        }
    }
    free(line);fclose(status);if (!found) fail("cannot establish launcher tracing state");
}
static void refuse_initial_capabilities(void) {
    struct __user_cap_header_struct header={.version=_LINUX_CAPABILITY_VERSION_3};
    struct __user_cap_data_struct data[2]={{0}};
    if (syscall(SYS_capget,&header,data)) fail("read initial host capabilities");
    for (size_t i=0;i<2;++i) {
        if (data[i].effective || data[i].permitted || data[i].inheritable) fail("unexpected initial host capabilities");
    }
    /* Ambient capabilities are a subset of both permitted and inheritable.
     * The ordinary host bounding set may be nonzero and grants nothing. */
}
int main(int argc,char **argv) {
    bool gvisor_outer=argc==2 && !strcmp(argv[1],"gvisor-outer-v1");
    if (prctl(PR_SET_DUMPABLE,0,0,0,0)) fail("clear launcher dumpability");
    if ((argc!=1 && !gvisor_outer) || getuid()==0 || geteuid()!=getuid() || getegid()!=getgid()) fail("launcher requires an ordinary unprivileged account and no arguments");
    refuse_initial_capabilities();
    struct rlimit core={0};if (setrlimit(RLIMIT_CORE,&core)) fail("disable core dumps");
    struct sigaction sa={.sa_handler=interrupted};sigemptyset(&sa.sa_mask);
    if (sigaction(SIGTERM,&sa,NULL) || sigaction(SIGINT,&sa,NULL) || sigaction(SIGHUP,&sa,NULL)) fail("install launcher termination handlers");
    if (prctl(PR_SET_PDEATHSIG,SIGTERM,0,0,0)) fail("arm launcher parent death");
    refuse_tracer();
    if (gvisor_outer) gvisor_outer_v1(&stopping);
    struct request r={0};read_request(&r);validate_sources(&r);
    uid_t uid=getuid();gid_t gid=getgid();
    int proc=open("/proc",O_PATH|O_DIRECTORY|O_CLOEXEC);if (proc<0) fail("pin genuine proc for original native owner");
    if (unshare(CLONE_NEWUSER)) fail("make outer user namespace");
    map("/proc/self/uid_map",0,uid);
    /* Drop the inherited host supplementary groups while the fresh outer
     * namespace still permits it. The fixed guest account must inherit no
     * unmapped host groups after both namespaces deny later changes. */
    if (setgroups(0,NULL)) fail("clear inherited supplementary groups");
    int groups=open("/proc/self/setgroups",O_WRONLY|O_CLOEXEC|O_NOFOLLOW);
    if (groups<0 || write(groups,"deny\n",5)!=5 || close(groups)) fail("deny group changes in outer namespace");
    map("/proc/self/gid_map",0,gid);
    if (prctl(PR_SET_DUMPABLE,0,0,0,0)) fail("clear dumpability after namespace mapping");
    if (unshare(CLONE_NEWNS|(r.version==1 ? CLONE_NEWPID:0)|CLONE_NEWNET|CLONE_NEWIPC|CLONE_NEWUTS|CLONE_NEWCGROUP)) fail("make private namespaces");
    if (sethostname("sandbox",7)) fail("set private hostname");
    int gate[2];if (pipe2(gate,O_CLOEXEC)) fail("make namespace activation pipe");
    int copy_channel[2]={-1,-1};
    if (r.version>=2 && !r.probe && socketpair(AF_UNIX,SOCK_SEQPACKET|SOCK_CLOEXEC,0,copy_channel)) fail("make original child copy-resource channel");
    pid_t pid;
    if (r.version>=2) {struct clone_args clone={.flags=CLONE_NEWPID,.exit_signal=SIGCHLD};pid=(pid_t)syscall(SYS_clone3,&clone,sizeof(clone));}
    else pid=fork();
    if (pid<0) fail("create owned namespace init");
    if (!pid) {close(gate[1]);if (copy_channel[0]>=0) close(copy_channel[0]);child(&r,gate[0],copy_channel[1]);}
    if (copy_channel[1]>=0) close(copy_channel[1]);
    close(gate[0]);
    int pidfd=(int)syscall(SYS_pidfd_open,pid,0);
    if (pidfd<0) {kill(pid,SIGKILL);waitpid(pid,NULL,0);fail("pin owned namespace init");}
    /* Only the exact unreaped child is signalled. The supervisor must own a
     * received pidfd and acknowledge it before any guest setup can begin. */
    if (!r.probe) wait_ack(pidfd,pid);
    if (stopping || write(gate[1],"G",1)!=1) syscall(SYS_pidfd_send_signal,pidfd,SIGKILL,NULL,0);
    close(4);close(5);
    int status=0;
    if (copy_channel[0]>=0) {
        struct copy_owner owner={0};copy_capture(&owner,&r,copy_channel[0],pidfd,proc);close(copy_channel[0]);
        status=copy_serve(&owner,pidfd,pid,&stopping);copy_release(&owner);close(6);
    } else for (;;) {
        if (stopping) syscall(SYS_pidfd_send_signal,pidfd,SIGKILL,NULL,0);
        pid_t got=waitpid(pid,&status,0);
        if (got==pid) break;
        if (got<0 && errno==EINTR) continue;
        syscall(SYS_pidfd_send_signal,pidfd,SIGKILL,NULL,0);fail("reap namespace init");
    }
    for (size_t i=0;i<r.count;++i) {close(r.mounts[i].fd);if (r.mounts[i].stage_fd) close(r.mounts[i].stage_fd);}
    close(proc);close(gate[1]);close(pidfd);
    if (WIFEXITED(status)) return WEXITSTATUS(status);
    return WIFSIGNALED(status) ? 128+WTERMSIG(status) : 1;
}
