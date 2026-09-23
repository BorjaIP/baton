package proto

// Version is the highest protocol version this build of the server and
// client supports.
const Version uint16 = 1

// MinVersion is the lowest protocol version this build supports.
const MinVersion uint16 = 1

// Negotiate returns the highest protocol version common to a client
// supporting [cMin, cMax] and a server supporting [sMin, sMax]. ok is false
// when the two ranges do not overlap, in which case the returned version is
// meaningless and the caller should report a version_mismatch error naming
// both ranges.
func Negotiate(cMin, cMax, sMin, sMax uint16) (uint16, bool) {
	lo := cMin
	if sMin > lo {
		lo = sMin
	}
	hi := cMax
	if sMax < hi {
		hi = sMax
	}
	if lo > hi {
		return 0, false
	}
	return hi, true
}
