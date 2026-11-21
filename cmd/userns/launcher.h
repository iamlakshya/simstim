/* SPDX-License-Identifier: Apache-2.0 */
#ifndef WALL_LAUNCHER_H
#define WALL_LAUNCHER_H
#define _GNU_SOURCE
#include <stdbool.h>
#include <signal.h>
#include <stddef.h>
#include <stdint.h>
#include <sys/types.h>
#define REQUEST_LIMIT (4U * 1024U * 1024U)
struct mount_request { int fd,stage_fd; char *guest,*stage_name; bool write; unsigned stage_fields; };
struct request { unsigned version; bool probe, dns; size_t count, capacity; struct mount_request *mounts; };
_Noreturn void fail(const char *what);
_Noreturn void gvisor_outer_v1(volatile sig_atomic_t *stopping);
struct msghdr;
struct cmsghdr;
struct cmsghdr *native_control_next(struct msghdr *message,size_t *cursor);
void read_request(struct request *request);
void validate_sources(const struct request *request);
int build_root(const struct request *request,int copy_channel);
void enter_root(int root);
void bring_up_loopback(void);
void enter_agent_user(void);
void confine(const struct request *request);
void close_setup_fds(void);
void close_creator_completion(void);
#endif
