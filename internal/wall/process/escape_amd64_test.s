//go:build linux && amd64

#include "textflag.h"

// func int80Unshare()
// Issues an i386 unshare(CLONE_NEWUSER) via the legacy 32-bit int 0x80 gate
// (i386 __NR_unshare = 310). Under our process-wall seccomp host-ABI guard,
// the kernel must immediately kill the calling process with SIGSYS.
TEXT ·int80Unshare(SB),NOSPLIT,$0-0
	MOVL $310, AX        // i386 __NR_unshare
	MOVL $0x10000000, BX // CLONE_NEWUSER
	INT  $0x80
	RET
