/* SPDX-License-Identifier: Apache-2.0 */
#include "launcher.h"
#include <sys/socket.h>

/* Both native receivers use this bounded libc-ABI walk. It avoids musl's
 * mixed-sign CMSG_NXTHDR expression without weakening compiler warnings. */
struct cmsghdr *native_control_next(struct msghdr *message,size_t *cursor) {
    if (*cursor==message->msg_controllen) return NULL;
    if (*cursor>message->msg_controllen || message->msg_controllen-*cursor<sizeof(struct cmsghdr)) fail("incomplete native ancillary header");
    struct cmsghdr *header=(struct cmsghdr *)((unsigned char *)message->msg_control+*cursor);
    size_t remaining=message->msg_controllen-*cursor,length=header->cmsg_len;
    if (length<CMSG_LEN(0) || length>remaining) fail("invalid bounded native ancillary length");
    size_t aligned=CMSG_ALIGN(length);
    if (aligned<length) fail("native ancillary alignment overflow");
    *cursor+=aligned<=remaining ? aligned:remaining;
    return header;
}
