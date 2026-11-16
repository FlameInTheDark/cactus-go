package audio

import "math"

// Resample resamples mono samples from one rate to another with a
// Kaiser-windowed sinc filter (~ -80 dB stopband, matching the quality
// class of the soxr resampler the Python reference uses). It is pure Go:
// the engine's fully-local contract forbids extra dependencies.
//
// The filter cutoff tracks min(0.5, 0.5*to/from) so downsampling cannot
// alias; the window half-width widens proportionally so the transition
// band stays narrow.
func Resample(samples []float32, from, to int) []float32 {
	if from <= 0 || to <= 0 || from == to || len(samples) == 0 {
		return samples
	}
	ratio := float64(to) / float64(from)
	n := int(math.Round(float64(len(samples)) * ratio))
	if n < 1 {
		n = 1
	}
	// Normalized cutoff (cycles per input sample); 0.97 leaves margin for
	// the finite transition band.
	cutoff := math.Min(0.5, 0.5*ratio) * 0.97
	if cutoff <= 0 || cutoff >= 0.5 {
		cutoff = 0.485
	}
	// Window half-width in input samples: about 8 sinc zero crossings on
	// each side of the interpolation point, scaled by the cutoff so the
	// transition band stays proportionally narrow when downsampling.
	half := math.Ceil(8 / cutoff)
	if half < 12 {
		half = 12
	}
	out := make([]float32, n)
	invI0Beta := 1 / besselI0(kaiserBeta)
	for j := 0; j < n; j++ {
		// Position of output j in input samples.
		t := float64(j) / ratio
		lo := int(math.Ceil(t - half))
		if lo < 0 {
			lo = 0
		}
		hi := int(math.Floor(t + half))
		if hi > len(samples)-1 {
			hi = len(samples) - 1
		}
		var sum float64
		for i := lo; i <= hi; i++ {
			s := float64(i) - t
			// Windowed sinc: 2*cutoff*sinc(2*cutoff*s) * kaiser(s/half).
			x := 2 * cutoff * s
			sinc := 1.0
			if x != 0 {
				sinc = math.Sin(math.Pi*x) / (math.Pi * x)
			}
			w := s / half
			window := besselI0(kaiserBeta*math.Sqrt(math.Max(0, 1-w*w))) * invI0Beta
			sum += float64(samples[i]) * 2 * cutoff * sinc * window
		}
		out[j] = float32(sum)
	}
	return out
}

// kaiserBeta targets ~ -80 dB sidelobes, soxr's default HQ class.
const kaiserBeta = 8.6

// besselI0 evaluates the modified Bessel function of the first kind,
// order zero, by its power series (|x| stays small in this package).
func besselI0(x float64) float64 {
	x = math.Abs(x)
	sum, term := 1.0, 1.0
	half := x / 2
	for k := 1; k < 60; k++ {
		term *= (half / float64(k)) * (half / float64(k))
		sum += term
		if term < 1e-18*sum {
			break
		}
	}
	return sum
}
