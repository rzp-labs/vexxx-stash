package transcoder

import (
	"fmt"
	"strings"

	"github.com/stashapp/stash/pkg/ffmpeg"
)

// IntelSpriteScreenshot encodes the accurately sought source frame directly
// through mjpeg_vaapi. The returned bytes are JPEG, never a CPU BMP transfer.
func IntelSpriteScreenshot(input string, seconds float64, plan ffmpeg.IntelGenerationPlan) ffmpeg.Args {
	args := ffmpeg.Args{"-v", "error", "-y", "-nostdin", "-abort_on", "empty_output"}
	args = append(args, plan.InputArgs...)
	args = args.Seek(seconds).Input(input)
	args = append(args, "-map", fmt.Sprintf("0:%d", plan.Source.StreamIndex), "-an", "-frames:v", "1", "-vf", plan.Filter+","+ffmpeg.IntelJPEGRangeFilter())
	return append(args, intelJPEGOutputArgs("-")...)
}

func intelJPEGOutputArgs(output string) ffmpeg.Args {
	return ffmpeg.Args{"-c:v", "mjpeg_vaapi", "-global_quality", "95", "-color_range", "tv", "-f", "image2", "-update", "1", output}
}

// IntelSpriteSheet preserves hardware surfaces through metadata-only selection,
// trim, split and PTS normalization. One source decoder processes independently
// sought concat segments; xstack_vaapi performs all pixel composition.
func IntelSpriteSheet(seekList string, plan ffmpeg.IntelGenerationPlan, count, columns, rows int, output string) (ffmpeg.Args, error) {
	if count <= 0 || columns <= 0 || rows <= 0 || count > columns*rows {
		return nil, fmt.Errorf("invalid GPU sprite grid")
	}
	args := ffmpeg.Args{"-v", "error", "-y", "-nostdin", "-abort_on", "empty_output", "-copyts"}
	args = append(args, plan.InputArgs...)
	args = append(args, "-f", "concat", "-safe", "0", "-protocol_whitelist", "file,crypto", "-segment_time_metadata", "1", "-i", seekList)
	// Pick the first frame in each one-second segment, using source timestamps.
	// concatdec_select excludes packet preroll, while trim:end excludes the next
	// segment if a requested frame is absent instead of substituting another tile.
	graph := fmt.Sprintf("[0:%d]select='concatdec_select*if(isnan(prev_pts),1,lt(prev_pts*TB,floor(t)))',%s", plan.Source.StreamIndex, plan.Filter)
	if count > 1 {
		graph += fmt.Sprintf(",split=%d", count)
		for i := 0; i < count; i++ {
			graph += fmt.Sprintf("[s%d]", i)
		}
	} else {
		graph += "[s0]"
	}
	partial := count < columns*rows
	for i := 0; i < count; i++ {
		graph += fmt.Sprintf(";[s%d]trim=start=%d:end=%d,trim=end_frame=1,setpts=PTS-STARTPTS", i, i, i+1)
		if partial && i == 0 {
			graph += ",split=2[t0][blacksource]"
		} else {
			graph += fmt.Sprintf("[t%d]", i)
		}
	}
	if partial {
		// Derive a limited-range black hardware tile without uploading pixels.
		graph += ";[blacksource]procamp_vaapi=c=0:b=0:s=0[blank]"
	}
	width, height := 0, 0
	// The last concrete scale defines tile dimensions; earlier stages preserve
	// detail during large reductions. Colour conversion then preserves iw/ih.
	for _, filter := range strings.Split(plan.Filter, ",") {
		if strings.HasPrefix(filter, "scale_vaapi=w=") {
			w, h := 0, 0
			_, _ = fmt.Sscanf(filter, "scale_vaapi=w=%d:h=%d", &w, &h)
			if w > 0 && h > 0 {
				width, height = w, h
			}
		}
	}
	if width <= 0 || height <= 0 {
		return nil, fmt.Errorf("GPU sprite plan has no tile dimensions")
	}
	// Compose one row at a time, then stack rows. Drivers impose a much
	// smaller per-composition surface limit than xstack_vaapi's parser maximum;
	// 81 simultaneous inputs can crash libva/iHD. The canonical 9x9 sheet never
	// asks either stage to compose more than nine GPU surfaces.
	rowCount := (count + columns - 1) / columns
	labels := make([]string, rowCount)
	for row := 0; row < rowCount; row++ {
		start := row * columns
		end := start + columns
		if end > count {
			end = count
		}
		graph += ";"
		if end-start == 1 {
			graph += fmt.Sprintf("[t%d]null", start)
		} else {
			layout := make([]string, end-start)
			for i := start; i < end; i++ {
				graph += fmt.Sprintf("[t%d]", i)
				layout[i-start] = fmt.Sprintf("%d_0", (i-start)*width)
			}
			graph += fmt.Sprintf("xstack_vaapi=inputs=%d:layout=%s:fill=black", end-start, strings.Join(layout, "|"))
		}
		labels[row] = fmt.Sprintf("row%d", row)
		graph += fmt.Sprintf("[%s]", labels[row])
	}
	graph += ";"
	if rowCount == 1 {
		graph += fmt.Sprintf("[%s]null", labels[0])
	} else {
		layout := make([]string, rowCount)
		for row, label := range labels {
			graph += fmt.Sprintf("[%s]", label)
			layout[row] = fmt.Sprintf("0_%d", row*height)
		}
		graph += fmt.Sprintf("xstack_vaapi=inputs=%d:layout=%s:fill=black", rowCount, strings.Join(layout, "|"))
	}
	// A black sentinel in the unused final cell forces canonical dimensions.
	// Unlike pad_vaapi, GPU composition works for tiny decoded source surfaces
	// on iHD; padding those surfaces can fail inside the JPEG encoder.
	if partial {
		graph += fmt.Sprintf("[content];[content][blank]xstack_vaapi=inputs=2:layout=0_0|%d_%d:fill=black", (columns-1)*width, (rows-1)*height)
	}
	graph += "," + ffmpeg.IntelJPEGRangeFilter() + "[sheet]"
	args = append(args, "-filter_complex", graph, "-map", "[sheet]", "-an", "-frames:v", "1")
	return append(args, intelJPEGOutputArgs(output)...), nil
}

// IntelSpriteSheetFrames selects canonical frame numbers for short clips using
// a single GPU decode. Duplicate requests share the same scaled GPU surface.
func IntelSpriteSheetFrames(input string, plan ffmpeg.IntelGenerationPlan, frames []int, columns, rows int, output string) (ffmpeg.Args, error) {
	positions := make([]int, len(frames))
	var unique []string
	for i, frame := range frames {
		if frame < 0 || (i > 0 && frame < frames[i-1]) {
			return nil, fmt.Errorf("invalid GPU sprite frame at index %d", i)
		}
		if i == 0 || frame != frames[i-1] {
			unique = append(unique, fmt.Sprintf("eq(n,%d)", frame))
		}
		positions[i] = len(unique) - 1
	}
	template, err := IntelSpriteSheet("unused", plan, len(frames), columns, rows, output)
	if err != nil {
		return nil, err
	}
	graph := ""
	for i, arg := range template {
		if arg == "-filter_complex" {
			graph = template[i+1]
			break
		}
	}
	old := fmt.Sprintf("[0:%d]select='concatdec_select*if(isnan(prev_pts),1,lt(prev_pts*TB,floor(t)))'", plan.Source.StreamIndex)
	graph = strings.Replace(graph, old, fmt.Sprintf("[0:%d]select='%s'", plan.Source.StreamIndex, strings.Join(unique, "+")), 1)
	for i, position := range positions {
		graph = strings.Replace(graph, fmt.Sprintf("[s%d]trim=start=%d:end=%d", i, i, i+1), fmt.Sprintf("[s%d]select='eq(n,%d)'", i, position), 1)
	}
	args := ffmpeg.Args{"-v", "error", "-y", "-nostdin", "-abort_on", "empty_output"}
	args = append(args, plan.InputArgs...)
	args = args.Input(input)
	args = append(args, "-filter_complex", graph, "-map", "[sheet]", "-an", "-frames:v", "1")
	return append(args, intelJPEGOutputArgs(output)...), nil
}
