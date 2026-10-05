package ffmpeg

import (
	"fmt"
	"strconv"
	"strings"
)

// intelParseDisplayMatrix retains FFprobe's complete fixed-point affine matrix.
// A malformed matrix cannot be replaced with its lossy scalar rotation.
func intelParseDisplayMatrix(value string) ([9]int32, error) {
	var matrix [9]int32
	lines := strings.Split(strings.TrimSpace(value), "\n")
	if len(lines) != 3 {
		return matrix, fmt.Errorf("GPU display matrix metadata is malformed")
	}
	for row, line := range lines {
		address, values, found := strings.Cut(line, ":")
		index, err := strconv.ParseUint(strings.TrimSpace(address), 16, 32)
		fields := strings.Fields(values)
		if !found || err != nil || index != uint64(row) || len(fields) != 3 {
			return matrix, fmt.Errorf("GPU display matrix row %d is malformed", row)
		}
		for col, field := range fields {
			v, err := strconv.ParseInt(field, 10, 32)
			if err != nil {
				return matrix, fmt.Errorf("GPU display matrix value %d is malformed", row*3+col)
			}
			matrix[row*3+col] = int32(v)
		}
	}
	return matrix, nil
}

// intelMatrixOrientation follows FFmpeg's canonical autorotation, including
// determinant-negative reflections. Translation is ignored by autorotation;
// perspective, skew and nonunit scaling require an additional GPU transform.
func intelMatrixOrientation(matrix [9]int32) (angle int, direction string, err error) {
	if matrix[2] != 0 || matrix[5] != 0 || matrix[8] != 1<<30 {
		return 0, "", fmt.Errorf("GPU display matrix perspective is unsupported")
	}
	switch [4]int32{matrix[0], matrix[1], matrix[3], matrix[4]} {
	case [4]int32{65536, 0, 0, 65536}:
		return 0, "", nil
	case [4]int32{-65536, 0, 0, -65536}:
		return 180, "reversal", nil
	case [4]int32{-65536, 0, 0, 65536}:
		return 180, "hflip", nil
	case [4]int32{65536, 0, 0, -65536}:
		return 0, "vflip", nil
	case [4]int32{0, 65536, -65536, 0}:
		return 90, "clock", nil
	case [4]int32{0, -65536, 65536, 0}:
		return 270, "cclock", nil
	case [4]int32{0, 65536, 65536, 0}:
		return 90, "cclock_flip", nil
	case [4]int32{0, -65536, -65536, 0}:
		return 270, "clock_flip", nil
	default:
		return 0, "", fmt.Errorf("GPU display matrix requires a unit orthogonal right-angle transform; skew/scaling/arbitrary rotation is unsupported")
	}
}

// intelRotationDegrees follows FFmpeg's autorotation convention: ffprobe's
// display-matrix rotation is the negative of the pixel transform. A complete
// matrix is authoritative; its reflection cannot be inferred from rotation.
func intelRotationDegrees(source IntelSource) (int, error) {
	if source.DisplayMatrix != nil {
		angle, _, err := intelMatrixOrientation(*source.DisplayMatrix)
		return angle, err
	}
	// Normalize before negating so even large legacy angles cannot overflow.
	angle := source.Rotation % 360
	if angle%90 != 0 {
		return 0, fmt.Errorf("GPU rotation supports only right-angle display matrices; got %d degrees", source.Rotation)
	}
	return (-angle + 360) % 360, nil
}

// IntelRotationFilter returns the resident pixel transform corresponding to
// canonical FFmpeg autorotation. Runtime probes verify the installed VPP/driver.
func IntelRotationFilter(config IntelGenerationConfig, source IntelSource) (string, error) {
	angle, err := intelRotationDegrees(source)
	if err != nil {
		return "", err
	}
	direction := ""
	if source.DisplayMatrix != nil {
		_, direction, _ = intelMatrixOrientation(*source.DisplayMatrix)
	} else {
		switch angle {
		case 90:
			direction = "clock"
		case 180:
			direction = "reversal"
		case 270:
			direction = "cclock"
		}
	}
	if direction == "" {
		return "", nil
	}
	if config.Backend != "vaapi" {
		return "", fmt.Errorf("GPU rotation has no validated resident primitive for backend %q", config.Backend)
	}
	return "transpose_vaapi=dir=" + direction + ":passthrough=none", nil
}

// IntelDisplayDimensions is the geometry after a validated right-angle pixel
// transform. Source width/height remain the physical decoder surface dimensions.
func IntelDisplayDimensions(source IntelSource) (width, height int) {
	width, height = source.Width, source.Height
	angle, _ := intelRotationDegrees(source)
	if angle == 90 || angle == 270 {
		width, height = height, width
	}
	return width, height
}

func intelPrependRotation(rotation, filter string) string {
	if rotation == "" {
		return filter
	}
	return rotation + "," + filter
}
