package publication

import (
	"bytes"
	"compress/zlib"
	"context"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"hash/crc32"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/image/font"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"

	reportmodel "intelligent-report-generation-system/internal/report"
)

const (
	documentCanvasWidth  = 1600
	documentPageHeight   = 1000
	documentCanvasMargin = 64
	maxDocumentHeight    = 12000
)

// RuntimeDocumentExportGenerator renders a deterministic, self-contained
// document from the immutable report definition and the same viewer-scoped
// result source used by CSV/XLSX. It is a safe availability fallback for the
// optional browser renderer: no lazy loading, no external resource fetches and
// no ungoverned query path.
type RuntimeDocumentExportGenerator struct {
	Source ExportResultSource
	Fonts  *DocumentFontSet
}

type DocumentFontSet struct {
	Regular []byte
}

// LoadDocumentFontSet loads a CJK-capable font without making the optional
// browser renderer a runtime dependency. Containers install Noto CJK at the
// first path below; macOS paths keep local development exports readable.
func LoadDocumentFontSet(explicitPath string) (*DocumentFontSet, error) {
	paths := []string{
		strings.TrimSpace(explicitPath),
		"/usr/share/fonts/opentype/noto/NotoSansCJK-Regular.ttc",
		"/usr/share/fonts/opentype/noto/NotoSansCJKsc-Regular.otf",
		"/System/Library/Fonts/STHeiti Medium.ttc",
		"/System/Library/Fonts/Hiragino Sans GB.ttc",
	}
	for index, path := range paths {
		if path == "" {
			continue
		}
		data, err := os.ReadFile(path)
		if err == nil {
			return &DocumentFontSet{Regular: data}, nil
		}
		if index == 0 && strings.TrimSpace(explicitPath) != "" {
			return nil, fmt.Errorf("load REPORT_EXPORT_FONT_PATH: %w", err)
		}
	}
	return nil, nil
}

func NewRuntimeDocumentExportGenerator(source ExportResultSource, fonts *DocumentFontSet) (*RuntimeDocumentExportGenerator, error) {
	if source == nil {
		return nil, errors.New("document export runtime is unavailable")
	}
	return &RuntimeDocumentExportGenerator{Source: source, Fonts: fonts}, nil
}

func (generator *RuntimeDocumentExportGenerator) Generate(ctx context.Context, claim ExportClaim, footer ExportFooter) (ExportArtifact, error) {
	if generator == nil || generator.Source == nil || (claim.Format != ExportPDF && claim.Format != ExportPNG) || claim.Definition.Validate() != nil {
		return ExportArtifact{}, errors.New("document report export is unavailable")
	}
	rows, err := generator.Source.Rows(ctx, claim)
	if err != nil {
		return ExportArtifact{}, err
	}
	canvas := newDocumentCanvas(generator.Fonts)
	if err := canvas.render(claim, footer, rows); err != nil {
		return ExportArtifact{}, err
	}
	var output []byte
	contentType := "image/png"
	if claim.Format == ExportPDF {
		output, err = encodeImagePDF(canvas.image)
		contentType = "application/pdf"
	} else {
		var buffer bytes.Buffer
		err = png.Encode(&buffer, canvas.image)
		output = buffer.Bytes()
	}
	if err != nil {
		return ExportArtifact{}, err
	}
	artifact := ExportArtifact{Bytes: output, ContentType: contentType, Extension: strings.ToLower(string(claim.Format)), Footer: footer}
	artifact.Seal()
	return artifact, nil
}

type exportCell struct {
	PageID, ComponentID, Role, Column string
	Row                               int
	Value                             any
	Partial                           bool
}

func decodeExportCells(rows ExportRows) ([]exportCell, error) {
	if len(rows.Columns) != 7 {
		return nil, errors.New("document export result columns are invalid")
	}
	result := make([]exportCell, 0, len(rows.Rows))
	for _, row := range rows.Rows {
		if len(row) != 7 {
			return nil, errors.New("document export result row is invalid")
		}
		index, err := strconv.Atoi(fmt.Sprint(row[3]))
		if err != nil {
			return nil, errors.New("document export row number is invalid")
		}
		result = append(result, exportCell{PageID: fmt.Sprint(row[0]), ComponentID: fmt.Sprint(row[1]), Role: fmt.Sprint(row[2]), Row: index, Column: fmt.Sprint(row[4]), Value: row[5], Partial: fmt.Sprint(row[6]) == "true"})
	}
	return result, nil
}

type documentCanvas struct {
	image                        *image.RGBA
	regular, medium, bold, small font.Face
	width                        int
}

func newDocumentCanvas(fonts *DocumentFontSet) *documentCanvas {
	regular, medium, bold, small := font.Face(nil), font.Face(nil), font.Face(nil), font.Face(nil)
	if fonts != nil && len(fonts.Regular) > 0 {
		if collection, err := opentype.ParseCollection(fonts.Regular); err == nil {
			if parsed, err := collection.Font(0); err == nil {
				regular, _ = opentype.NewFace(parsed, &opentype.FaceOptions{Size: 16, DPI: 96, Hinting: font.HintingFull})
				medium, _ = opentype.NewFace(parsed, &opentype.FaceOptions{Size: 20, DPI: 96, Hinting: font.HintingFull})
				bold, _ = opentype.NewFace(parsed, &opentype.FaceOptions{Size: 30, DPI: 96, Hinting: font.HintingFull})
				small, _ = opentype.NewFace(parsed, &opentype.FaceOptions{Size: 12, DPI: 96, Hinting: font.HintingFull})
			}
		}
	}
	return &documentCanvas{regular: regular, medium: medium, bold: bold, small: small, width: documentCanvasWidth}
}

func (canvas *documentCanvas) render(claim ExportClaim, footer ExportFooter, rows ExportRows) error {
	cells, err := decodeExportCells(rows)
	if err != nil {
		return err
	}
	selected := map[string]bool{}
	for _, pageID := range claim.PageIDs {
		selected[string(pageID)] = true
	}
	pages := append([]reportmodel.Page(nil), claim.Definition.Pages...)
	sort.SliceStable(pages, func(i, j int) bool { return pages[i].Order < pages[j].Order })
	visiblePages := make([]reportmodel.Page, 0, len(pages))
	for _, page := range pages {
		if len(selected) == 0 || selected[string(page.ID)] {
			visiblePages = append(visiblePages, page)
		}
	}
	height := 190
	for _, page := range visiblePages {
		height += canvas.pageHeight(page, claim.Definition.Canvas.Desktop) + 46
	}
	height += 110
	height = min(max(height, 520), maxDocumentHeight)
	canvas.image = image.NewRGBA(image.Rect(0, 0, canvas.width, height))
	draw.Draw(canvas.image, canvas.image.Bounds(), image.NewUniform(color.RGBA{246, 249, 253, 255}), image.Point{}, draw.Src)
	canvas.fill(image.Rect(0, 0, canvas.width, 132), color.RGBA{255, 255, 255, 255})
	canvas.fill(image.Rect(0, 128, canvas.width, 132), color.RGBA{21, 110, 206, 255})
	canvas.text(documentCanvasMargin, 54, claim.Definition.Metadata.Name, canvas.bold, color.RGBA{35, 51, 72, 255})
	canvas.text(documentCanvasMargin, 91, claim.Definition.Metadata.Description, canvas.regular, color.RGBA{104, 119, 140, 255})
	canvas.text(canvas.width-500, 51, fmt.Sprintf("V%d · %s", footer.ReportVersion, footer.AsOf), canvas.regular, color.RGBA{78, 96, 120, 255})
	canvas.text(canvas.width-500, 84, "可信数据 · 权限隔离 · 版本冻结", canvas.small, color.RGBA{124, 139, 158, 255})

	componentByID := make(map[string]reportmodel.Component, len(claim.Definition.Components))
	for _, component := range claim.Definition.Components {
		componentByID[string(component.ID)] = component
	}
	cellsByComponent := map[string][]exportCell{}
	for _, cell := range cells {
		cellsByComponent[cell.ComponentID] = append(cellsByComponent[cell.ComponentID], cell)
	}
	y := 168
	for _, page := range visiblePages {
		pageHeight := canvas.pageHeight(page, claim.Definition.Canvas.Desktop)
		pageRect := image.Rect(documentCanvasMargin, y, canvas.width-documentCanvasMargin, y+pageHeight)
		canvas.fill(pageRect, color.RGBA{255, 255, 255, 255})
		canvas.stroke(pageRect, color.RGBA{219, 229, 241, 255})
		canvas.text(pageRect.Min.X+24, pageRect.Min.Y+35, page.Name, canvas.medium, color.RGBA{42, 58, 81, 255})
		canvas.line(pageRect.Min.X+24, pageRect.Min.Y+52, pageRect.Max.X-24, pageRect.Min.Y+52, color.RGBA{230, 236, 244, 255})
		contentTop := pageRect.Min.Y + 70
		for _, section := range page.Sections {
			for _, block := range section.Blocks {
				rect := canvas.blockRect(block, claim.Definition.Canvas.Desktop, pageRect, contentTop)
				if rect.Max.Y > pageRect.Max.Y-20 {
					continue
				}
				canvas.renderBlock(rect, block, componentByID, cellsByComponent)
			}
		}
		y += pageHeight + 46
		if y > height-100 {
			break
		}
	}
	filterJSON := "无"
	if len(footer.Filters) > 0 {
		parts := make([]string, 0, len(footer.Filters))
		for key, value := range footer.Filters {
			parts = append(parts, key+"="+fmt.Sprint(value))
		}
		sort.Strings(parts)
		filterJSON = strings.Join(parts, "；")
	}
	canvas.line(documentCanvasMargin, height-82, canvas.width-documentCanvasMargin, height-82, color.RGBA{215, 225, 237, 255})
	canvas.text(documentCanvasMargin, height-48, "导出时间 "+footer.ExportedAt+"  ·  筛选 "+truncateRunes(filterJSON, 80), canvas.small, color.RGBA{110, 124, 144, 255})
	canvas.text(canvas.width-370, height-48, "智能分析决策平台", canvas.small, color.RGBA{24, 111, 205, 255})
	return nil
}

func (canvas *documentCanvas) pageHeight(page reportmodel.Page, desktop reportmodel.DesktopCanvas) int {
	maxBottom := 1
	for _, section := range page.Sections {
		for _, block := range section.Blocks {
			maxBottom = max(maxBottom, block.Layout.Desktop.Y+block.Layout.Desktop.H)
		}
	}
	return min(max(240+maxBottom*(max(desktop.BaseRowHeight, 36)+max(desktop.GapY, 8)), 520), 2400)
}

func (canvas *documentCanvas) blockRect(block reportmodel.Block, desktop reportmodel.DesktopCanvas, pageRect image.Rectangle, contentTop int) image.Rectangle {
	columns := max(desktop.Columns, 1)
	gapX, gapY := max(desktop.GapX, 0), max(desktop.GapY, 0)
	usable := pageRect.Dx() - 48
	cellWidth := float64(usable-gapX*(columns-1)) / float64(columns)
	x := pageRect.Min.X + 24 + int(math.Round(float64(block.Layout.Desktop.X)*float64(gapX)+float64(block.Layout.Desktop.X)*cellWidth))
	y := contentTop + block.Layout.Desktop.Y*(max(desktop.BaseRowHeight, 36)+gapY)
	w := int(math.Round(float64(block.Layout.Desktop.W)*cellWidth)) + max(block.Layout.Desktop.W-1, 0)*gapX
	h := block.Layout.Desktop.H*max(desktop.BaseRowHeight, 36) + max(block.Layout.Desktop.H-1, 0)*gapY
	return image.Rect(x, y, x+max(w, 160), y+max(h, 100))
}

func (canvas *documentCanvas) renderBlock(rect image.Rectangle, block reportmodel.Block, components map[string]reportmodel.Component, cells map[string][]exportCell) {
	canvas.fill(rect, color.RGBA{250, 252, 255, 255})
	canvas.stroke(rect, color.RGBA{215, 226, 239, 255})
	componentIDs := make([]string, 0, 4)
	for _, zone := range block.Zones {
		for _, slot := range zone.Slots {
			if slot.ComponentID != "" {
				componentIDs = append(componentIDs, string(slot.ComponentID))
			}
		}
	}
	if len(componentIDs) == 0 {
		canvas.text(rect.Min.X+18, rect.Min.Y+35, "空内容区", canvas.regular, color.RGBA{123, 137, 156, 255})
		return
	}
	component := components[componentIDs[0]]
	componentCells := cells[componentIDs[0]]
	title := component.Options.Title
	if title == "" {
		title = component.TemplateRef.Type
	}
	canvas.text(rect.Min.X+18, rect.Min.Y+31, truncateRunes(title, 42), canvas.regular, color.RGBA{43, 60, 84, 255})
	canvas.line(rect.Min.X+18, rect.Min.Y+44, rect.Max.X-18, rect.Min.Y+44, color.RGBA{228, 235, 243, 255})
	canvas.renderComponent(rect.Inset(18), component, componentCells)
}

func (canvas *documentCanvas) renderComponent(rect image.Rectangle, component reportmodel.Component, cells []exportCell) {
	contentTop := rect.Min.Y + 48
	typeName := component.TemplateRef.Type
	if typeName == "rich-text" || typeName == "insight-text" {
		canvas.wrappedText(rect.Min.X, contentTop+8, rect.Dx(), plainRichText(component.Options.RichText), canvas.regular, color.RGBA{69, 86, 109, 255}, 26, max((rect.Dy()-50)/26, 1))
		return
	}
	rows := groupCells(cells)
	if len(rows) == 0 {
		canvas.text(rect.Min.X, contentTop+20, "当前筛选范围暂无数据", canvas.small, color.RGBA{126, 140, 158, 255})
		return
	}
	if typeName == "metric-card" {
		value, label := firstCell(rows), firstColumn(rows)
		canvas.text(rect.Min.X, contentTop+48, truncateRunes(value, 24), canvas.bold, color.RGBA{21, 110, 206, 255})
		canvas.text(rect.Min.X, contentTop+79, truncateRunes(label, 30), canvas.small, color.RGBA{112, 127, 147, 255})
		return
	}
	if typeName == "data-table" {
		canvas.renderTable(image.Rect(rect.Min.X, contentTop, rect.Max.X, rect.Max.Y), rows)
		return
	}
	canvas.renderChart(image.Rect(rect.Min.X, contentTop, rect.Max.X, rect.Max.Y), rows, typeName)
}

func groupCells(cells []exportCell) []map[string]any {
	grouped := map[int]map[string]any{}
	indexes := []int{}
	for _, cell := range cells {
		if grouped[cell.Row] == nil {
			grouped[cell.Row] = map[string]any{}
			indexes = append(indexes, cell.Row)
		}
		grouped[cell.Row][cell.Column] = cell.Value
	}
	sort.Ints(indexes)
	result := make([]map[string]any, 0, len(indexes))
	seen := map[int]bool{}
	for _, index := range indexes {
		if !seen[index] {
			result, seen[index] = append(result, grouped[index]), true
		}
	}
	return result
}

func (canvas *documentCanvas) renderTable(rect image.Rectangle, rows []map[string]any) {
	columns := sortedColumns(rows)
	if len(columns) == 0 {
		return
	}
	columns = columns[:min(len(columns), 6)]
	rowHeight := 32
	columnWidth := max(rect.Dx()/len(columns), 80)
	canvas.fill(image.Rect(rect.Min.X, rect.Min.Y, rect.Max.X, rect.Min.Y+rowHeight), color.RGBA{237, 245, 255, 255})
	for index, column := range columns {
		canvas.text(rect.Min.X+index*columnWidth+8, rect.Min.Y+21, truncateRunes(column, 16), canvas.small, color.RGBA{55, 79, 111, 255})
	}
	limit := min(len(rows), max((rect.Dy()-rowHeight)/rowHeight, 1))
	for rowIndex := 0; rowIndex < limit; rowIndex++ {
		y := rect.Min.Y + (rowIndex+1)*rowHeight
		if rowIndex%2 == 1 {
			canvas.fill(image.Rect(rect.Min.X, y, rect.Max.X, y+rowHeight), color.RGBA{248, 251, 254, 255})
		}
		for columnIndex, column := range columns {
			canvas.text(rect.Min.X+columnIndex*columnWidth+8, y+21, truncateRunes(fmt.Sprint(rows[rowIndex][column]), 18), canvas.small, color.RGBA{79, 94, 116, 255})
		}
	}
}

func (canvas *documentCanvas) renderChart(rect image.Rectangle, rows []map[string]any, typeName string) {
	columns := sortedColumns(rows)
	valueColumn := ""
	for _, column := range columns {
		for _, row := range rows {
			if _, err := strconv.ParseFloat(fmt.Sprint(row[column]), 64); err == nil {
				valueColumn = column
				break
			}
		}
		if valueColumn != "" {
			break
		}
	}
	if valueColumn == "" {
		canvas.renderTable(rect, rows)
		return
	}
	labelColumn := columns[0]
	if labelColumn == valueColumn && len(columns) > 1 {
		labelColumn = columns[1]
	}
	values := make([]float64, min(len(rows), 12))
	maximum := 0.0
	for index := range values {
		values[index], _ = strconv.ParseFloat(fmt.Sprint(rows[index][valueColumn]), 64)
		maximum = max(maximum, math.Abs(values[index]))
	}
	if maximum == 0 {
		maximum = 1
	}
	chartLeft, chartBottom := rect.Min.X+8, rect.Max.Y-26
	chartTop, chartRight := rect.Min.Y+8, rect.Max.X-8
	canvas.line(chartLeft, chartTop, chartLeft, chartBottom, color.RGBA{196, 210, 226, 255})
	canvas.line(chartLeft, chartBottom, chartRight, chartBottom, color.RGBA{196, 210, 226, 255})
	barWidth := max((chartRight-chartLeft)/max(len(values), 1)-12, 10)
	points := make([]image.Point, 0, len(values))
	for index, value := range values {
		x := chartLeft + index*(barWidth+12) + 8
		height := int(math.Round(math.Abs(value) / maximum * float64(max(chartBottom-chartTop-38, 20))))
		bar := image.Rect(x, chartBottom-height, x+barWidth, chartBottom)
		canvas.fill(bar, color.RGBA{57, 139, 231, 220})
		points = append(points, image.Pt(x+barWidth/2, chartBottom-height))
		canvas.text(x, chartBottom+19, truncateRunes(fmt.Sprint(rows[index][labelColumn]), 7), canvas.small, color.RGBA{109, 124, 144, 255})
	}
	if strings.Contains(typeName, "line") || strings.Contains(typeName, "area") {
		for index := 1; index < len(points); index++ {
			canvas.thickLine(points[index-1], points[index], color.RGBA{18, 100, 196, 255})
		}
	}
}

func firstCell(rows []map[string]any) string {
	columns := sortedColumns(rows)
	if len(columns) == 0 {
		return "—"
	}
	return fmt.Sprint(rows[0][columns[0]])
}

func firstColumn(rows []map[string]any) string {
	columns := sortedColumns(rows)
	if len(columns) == 0 {
		return "指标值"
	}
	return columns[0]
}

func sortedColumns(rows []map[string]any) []string {
	set := map[string]bool{}
	for _, row := range rows {
		for column := range row {
			set[column] = true
		}
	}
	result := make([]string, 0, len(set))
	for column := range set {
		result = append(result, column)
	}
	sort.Strings(result)
	return result
}

func plainRichText(value string) string {
	fragment, err := html.ParseFragment(strings.NewReader(value), &html.Node{Type: html.ElementNode, Data: "div", DataAtom: atom.Div})
	if err != nil {
		return value
	}
	var output strings.Builder
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		if node.Type == html.TextNode {
			output.WriteString(node.Data)
		}
		if node.Type == html.ElementNode && (node.Data == "p" || node.Data == "li" || node.Data == "br") && output.Len() > 0 {
			output.WriteByte('\n')
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	for _, node := range fragment {
		walk(node)
	}
	return strings.TrimSpace(output.String())
}

func (canvas *documentCanvas) text(x, baseline int, value string, face font.Face, colour color.Color) {
	if value == "" || face == nil {
		return
	}
	drawer := font.Drawer{Dst: canvas.image, Src: image.NewUniform(colour), Face: face, Dot: fixed.P(x, baseline)}
	drawer.DrawString(value)
}

func (canvas *documentCanvas) wrappedText(x, y, width int, value string, face font.Face, colour color.Color, lineHeight, maxLines int) {
	if face == nil {
		return
	}
	var line strings.Builder
	lineNo := 0
	flush := func() {
		if line.Len() == 0 || lineNo >= maxLines {
			return
		}
		canvas.text(x, y+lineNo*lineHeight, line.String(), face, colour)
		line.Reset()
		lineNo++
	}
	for _, runeValue := range value {
		if runeValue == '\n' {
			flush()
			continue
		}
		candidate := line.String() + string(runeValue)
		if font.MeasureString(face, candidate).Ceil() > width && line.Len() > 0 {
			flush()
		}
		if lineNo >= maxLines {
			break
		}
		line.WriteRune(runeValue)
	}
	flush()
}

func (canvas *documentCanvas) fill(rect image.Rectangle, colour color.Color) {
	draw.Draw(canvas.image, rect.Intersect(canvas.image.Bounds()), image.NewUniform(colour), image.Point{}, draw.Src)
}

func (canvas *documentCanvas) stroke(rect image.Rectangle, colour color.Color) {
	canvas.line(rect.Min.X, rect.Min.Y, rect.Max.X, rect.Min.Y, colour)
	canvas.line(rect.Min.X, rect.Max.Y-1, rect.Max.X, rect.Max.Y-1, colour)
	canvas.line(rect.Min.X, rect.Min.Y, rect.Min.X, rect.Max.Y, colour)
	canvas.line(rect.Max.X-1, rect.Min.Y, rect.Max.X-1, rect.Max.Y, colour)
}

func (canvas *documentCanvas) line(x1, y1, x2, y2 int, colour color.Color) {
	if x1 == x2 {
		canvas.fill(image.Rect(x1, min(y1, y2), x1+1, max(y1, y2)+1), colour)
		return
	}
	if y1 == y2 {
		canvas.fill(image.Rect(min(x1, x2), y1, max(x1, x2)+1, y1+1), colour)
		return
	}
	canvas.thickLine(image.Pt(x1, y1), image.Pt(x2, y2), colour)
}

func (canvas *documentCanvas) thickLine(from, to image.Point, colour color.Color) {
	deltaX, deltaY := int(math.Abs(float64(to.X-from.X))), int(math.Abs(float64(to.Y-from.Y)))
	steps := max(deltaX, deltaY)
	if steps == 0 {
		canvas.fill(image.Rect(from.X, from.Y, from.X+2, from.Y+2), colour)
		return
	}
	for step := 0; step <= steps; step++ {
		x := from.X + (to.X-from.X)*step/steps
		y := from.Y + (to.Y-from.Y)*step/steps
		canvas.fill(image.Rect(x-1, y-1, x+2, y+2), colour)
	}
}

func truncateRunes(value string, limit int) string {
	runes := []rune(strings.TrimSpace(value))
	if len(runes) <= limit {
		return string(runes)
	}
	return string(runes[:max(limit-1, 1)]) + "…"
}

func encodeImagePDF(source image.Image) ([]byte, error) {
	bounds := source.Bounds()
	width, height := bounds.Dx(), bounds.Dy()
	if width < 1 || height < 1 {
		return nil, errors.New("document export image is empty")
	}
	const pageWidth, pageHeight = 842.0, 595.0
	pixelPageHeight := max(int(math.Floor(float64(width)*pageHeight/pageWidth)), 1)
	pageCount := (height + pixelPageHeight - 1) / pixelPageHeight
	objects := make([][]byte, 2+pageCount*3)
	objects[0] = []byte("<< /Type /Catalog /Pages 2 0 R >>")
	kids := make([]string, 0, pageCount)
	for page := 0; page < pageCount; page++ {
		pageObjectID := 3 + page*3
		kids = append(kids, fmt.Sprintf("%d 0 R", pageObjectID))
	}
	objects[1] = []byte(fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d >>", strings.Join(kids, " "), pageCount))
	for page := 0; page < pageCount; page++ {
		pageObjectID := 3 + page*3
		contentObjectID := pageObjectID + 1
		imageObjectID := pageObjectID + 2
		startY := bounds.Min.Y + page*pixelPageHeight
		endY := min(startY+pixelPageHeight, bounds.Max.Y)
		sliceHeight := endY - startY
		raw := make([]byte, 0, width*sliceHeight*3)
		for y := startY; y < endY; y++ {
			for x := bounds.Min.X; x < bounds.Max.X; x++ {
				r, g, b, _ := source.At(x, y).RGBA()
				raw = append(raw, byte(r>>8), byte(g>>8), byte(b>>8))
			}
		}
		var compressed bytes.Buffer
		writer, _ := zlib.NewWriterLevel(&compressed, zlib.BestSpeed)
		if _, err := writer.Write(raw); err != nil {
			return nil, err
		}
		if err := writer.Close(); err != nil {
			return nil, err
		}
		renderedHeight := pageWidth * float64(sliceHeight) / float64(width)
		content := fmt.Sprintf("q %.2f 0 0 %.2f 0 %.2f cm /Im0 Do Q\n", pageWidth, renderedHeight, pageHeight-renderedHeight)
		objects[pageObjectID-1] = []byte(fmt.Sprintf("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 %.2f %.2f] /Resources << /XObject << /Im0 %d 0 R >> >> /Contents %d 0 R >>", pageWidth, pageHeight, imageObjectID, contentObjectID))
		objects[contentObjectID-1] = []byte(fmt.Sprintf("<< /Length %d >>\nstream\n%sendstream", len(content), content))
		imageHeader := fmt.Sprintf("<< /Type /XObject /Subtype /Image /Width %d /Height %d /ColorSpace /DeviceRGB /BitsPerComponent 8 /Filter /FlateDecode /Length %d >>\nstream\n", width, sliceHeight, compressed.Len())
		objects[imageObjectID-1] = append(append([]byte(imageHeader), compressed.Bytes()...), []byte("\nendstream")...)
	}
	var pdf bytes.Buffer
	pdf.WriteString("%PDF-1.4\n%\xe2\xe3\xcf\xd3\n")
	offsets := make([]int, len(objects)+1)
	for index, object := range objects {
		offsets[index+1] = pdf.Len()
		fmt.Fprintf(&pdf, "%d 0 obj\n", index+1)
		pdf.Write(object)
		pdf.WriteString("\nendobj\n")
	}
	xref := pdf.Len()
	fmt.Fprintf(&pdf, "xref\n0 %d\n0000000000 65535 f \n", len(offsets))
	for index := 1; index < len(offsets); index++ {
		fmt.Fprintf(&pdf, "%010d 00000 n \n", offsets[index])
	}
	fmt.Fprintf(&pdf, "trailer\n<< /Size %d /Root 1 0 R /ID [<%s><%s>] >>\nstartxref\n%d\n%%%%EOF\n", len(offsets), pdfID(pdf.Bytes()), pdfID(pdf.Bytes()), xref)
	return pdf.Bytes(), nil
}

func pdfID(value []byte) string {
	var bytes16 [16]byte
	binary.BigEndian.PutUint32(bytes16[0:4], crc32.ChecksumIEEE(value))
	binary.BigEndian.PutUint32(bytes16[4:8], uint32(len(value)))
	binary.BigEndian.PutUint32(bytes16[8:12], crc32.Checksum(value, crc32.MakeTable(crc32.Castagnoli)))
	binary.BigEndian.PutUint32(bytes16[12:16], uint32(len(value))*2654435761)
	return hex.EncodeToString(bytes16[:])
}
