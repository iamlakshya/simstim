#include "textflag.h"

// The real i386 compatibility syscall must not bypass the seccomp ABI guard.
TEXT ·compatUnshareCall(SB),NOSPLIT,$0-4
	MOVL $310, AX
	MOVL $0x10000000, BX
	INT $0x80
	MOVL AX, ret+0(FP)
	RET
