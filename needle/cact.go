package needle

import (
	"encoding/binary"
	"math"
	"os"
)

// Confidence-head detection: a tuned Needle 3 archive only reports a
// calibrated confidence score when it carries the post-hoc calibration
// head trained for those weights. The head is a small fp16 tensor of one,
// two or three codes sitting in the .cact tensor directory just before
// the final raw (tokenizer) record; code 2 marks the confidence head.
// This mirrors _confidence_head_present in the Python reference.

// confidenceHeadCode marks the confidence head inside the head tensor.
const confidenceHeadCode = 2

// cactConfidenceHeadPresent reports whether a .cact archive carries a
// trained confidence head. Only the header and the tensor directory are
// read; the payload is never loaded.
func cactConfidenceHeadPresent(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()

	header := make([]byte, 196)
	if n, err := f.Read(header); err != nil || n < len(header) {
		return false
	}
	count := int(binary.LittleEndian.Uint32(header[4:8]))
	codebook := int(binary.LittleEndian.Uint32(header[8:12]))
	if count <= 0 || count > 1<<20 {
		return false
	}
	directory := make([]byte, count*44)
	if _, err := f.ReadAt(directory, int64(196+codebook*4)); err != nil {
		return false
	}
	// The last record must be the raw tokenizer blob.
	dtype := directory[(count-1)*44]
	if dtype != 4 {
		return false
	}
	record := func(i int) (dtype uint8, ndim uint8, d0 uint32, offset uint64) {
		rec := directory[i*44 : (i+1)*44]
		return rec[0], rec[1], binary.LittleEndian.Uint32(rec[4:8]), binary.LittleEndian.Uint64(rec[20:28])
	}
	for heads := 1; heads <= 3; heads++ {
		index := count - 2 - 6*heads
		if index < 0 {
			continue
		}
		dt, ndim, length, offset := record(index)
		// A 1-D fp16 tensor of exactly `heads` values, right where the
		// head manifest lives.
		if dt != 1 || ndim != 1 || length != uint32(heads) {
			continue
		}
		codes := make([]byte, 2*heads)
		if _, err := f.ReadAt(codes, int64(offset)); err != nil {
			return false
		}
		for i := 0; i < heads; i++ {
			if int(math.Round(float64(fp16tof32(binary.LittleEndian.Uint16(codes[i*2:]))))) == confidenceHeadCode {
				return true
			}
		}
		return false
	}
	return false
}

// fp16tof32 decodes an IEEE 754 half-precision value.
func fp16tof32(h uint16) float32 {
	sign := uint32(h&0x8000) << 16
	exp := uint32(h>>10) & 0x1F
	frac := uint32(h) & 0x3FF
	switch {
	case exp == 0: // zero or subnormal
		if frac == 0 {
			return math.Float32frombits(sign)
		}
		// Normalize the subnormal.
		e := uint32(127 - 15 + 1)
		for frac&0x400 == 0 {
			frac <<= 1
			e--
		}
		frac &= 0x3FF
		return math.Float32frombits(sign | e<<23 | frac<<13)
	case exp == 0x1F: // infinity or NaN
		if frac == 0 {
			return math.Float32frombits(sign | 0x7F800000)
		}
		return math.Float32frombits(sign | 0x7F800000 | frac<<13)
	}
	return math.Float32frombits(sign | (exp+112)<<23 | frac<<13)
}
