package proxy

// SO_REUSEPORT = 15 (asm-generic/socket.h; x86/arm/arm64). Gói syscall của Go
// không export hằng này và repo không kéo golang.org/x/sys chỉ vì một số.
const soReusePort = 0xf
