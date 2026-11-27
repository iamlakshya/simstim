/* SPDX-License-Identifier: Apache-2.0 */
#ifndef WALL_COPY_H
#define WALL_COPY_H
#include "launcher.h"
#include <limits.h>
#include <signal.h>
#include <sys/stat.h>
struct copy_mount { uint32_t index; int root, stage, cleanup_root; char *prefix, *owned_name; struct stat identity; };
struct copy_owner { const struct request *request; int root, proc; struct copy_mount *mounts; size_t count; };
struct copy_transaction { struct copy_mount *mount; int file; char name[48], target[PATH_MAX]; bool replace; struct stat identity; };
bool valid_guest_path(const char *path);
bool copy_same_mount(int a,int b);
bool copy_contains(int source,int descendant);
void copy_prepare(int root,const struct request *request,int channel);
void copy_capture(struct copy_owner *owner,const struct request *request,int channel,int init_pidfd,int proc);
void copy_release(struct copy_owner *owner);
int copy_publish(struct copy_owner *owner,struct copy_transaction *transaction,int parent_gate);
int copy_serve(struct copy_owner *owner,int init_pidfd,pid_t init_pid,volatile sig_atomic_t *stopping);
#endif
