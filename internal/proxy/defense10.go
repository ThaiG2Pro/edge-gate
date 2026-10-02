//go:build !nodefense10

package proxy

// h2LimitBodyToCL (phase 10 D6 c, lớp hai): body stream h2 có content-length
// ⇒ chép đúng CL byte sang upstream rồi đòi EOF. nodefense10 ⇒ chép mọi DATA.
const h2LimitBodyToCL = true
