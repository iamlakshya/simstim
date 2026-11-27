package client

// Darwin sys/filio.h defines FIONREAD as _IOR('f', 127, int).
const agentNativeFIONREAD = 0x4004667f
