package chunker

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/MedAIst-Studio/MedAIist-Backend/domain"
	"github.com/google/uuid"
	"github.com/ledongthuc/pdf"
)

var (
	ErrLowQualityPDF    = errors.New("PDF kalitesi düşük veya metin çıkarılamadı")
	ErrCorruptedPDF     = errors.New("PDF bozuk veya okunamıyor")
	ErrEmptyPDF         = errors.New("PDF boş veya metin içermiyor")
	ErrScannedPDF       = errors.New("PDF taranmış görüntü içeriyor (OCR gerekli)")
	ErrInsufficientText = errors.New("PDF'de yeterli metin bulunamadı")
)

type ChunkingService interface {
	ChunkPDF(ctx context.Context, fileBytes []byte, fileID string, options domain.ChunkOptions) ([]domain.Chunk, error)
}

type PdfChunkingService struct {
	abbreviations map[string]bool
	Config        *domain.ChunkOptions

	// Advanced patterns for medical/academic documents
	sectionHeaders  *regexp.Regexp
	listMarkers     *regexp.Regexp
	citationPattern *regexp.Regexp
	emailPattern    *regexp.Regexp
	urlPattern      *regexp.Regexp
}

type QualityMetrics struct {
	TotalPages        int
	ExtractedPages    int
	FailedPages       []int
	TotalChars        int
	TotalWords        int
	LetterRatio       float64
	AverageWordLength float64
	GarbageRatio      float64
	PunctuationRatio  float64
	AvgChunkQuality   float64
	IsHighQuality     bool
	QualityIssues     []string
}

func NewChunkingService(config *domain.ChunkOptions) *PdfChunkingService {
	// Comprehensive abbreviations for medical/academic/business contexts
	abbreviations := map[string]bool{
		// Titles
		"dr": true, "mr": true, "mrs": true, "ms": true, "prof": true,
		"sr": true, "jr": true, "ph": true, "md": true, "phd": true,
		"dds": true, "esq": true, "rev": true, "hon": true,

		// Academic/Medical
		"fig": true, "eq": true, "vol": true, "no": true, "pp": true,
		"ed": true, "eds": true, "dept": true, "univ": true, "approx": true,
		"temp": true, "max": true, "min": true, "avg": true, "std": true,
		"vs": true, "et": true, "al": true, "ibid": true, "op": true,
		"cit": true, "loc": true, "ca": true, "cf": true,

		// Business/Legal
		"inc": true, "ltd": true, "co": true, "corp": true, "llc": true,
		"etc": true, "assn": true, "bros": true, "mgmt": true,

		// Common Latin
		"e.g": true, "i.e": true, "viz": true, "n.b": true, "p.s": true,

		// Time/Location
		"jan": true, "feb": true, "mar": true, "apr": true, "jun": true,
		"jul": true, "aug": true, "sep": true, "oct": true, "nov": true,
		"dec": true, "st": true, "rd": true, "ave": true, "blvd": true,
	}

	// Optimal defaults for RAG systems (based on embedding model limits)
	if config.MaxChunkSize == 0 {
		config.MaxChunkSize = 800 // Optimal for most embedding models (512-1024 tokens)
	}
	if config.OverlapSize == 0 {
		config.OverlapSize = 150 // 15-20% overlap maintains context continuity
	}

	return &PdfChunkingService{
		abbreviations:   abbreviations,
		Config:          config,
		sectionHeaders:  regexp.MustCompile(`(?m)^(?:[A-Z][A-Z\s]{2,}|(?:\d+\.)+\s*[A-Z]|[IVX]+\.\s+[A-Z])`),
		listMarkers:     regexp.MustCompile(`(?m)^[\s]*(?:[-*•○●]|\d+\.|\(?[a-z]\)|\([0-9]+\))\s+`),
		citationPattern: regexp.MustCompile(`\[\d+(?:,\s*\d+)*\]|\(\w+(?:\s+et\s+al\.?)?,?\s+\d{4}\)`),
		emailPattern:    regexp.MustCompile(`[a-zA-Z0-9._%+-]+@[a-zA-Z0-9.-]+\.[a-zA-Z]{2,}`),
		urlPattern:      regexp.MustCompile(`https?://[^\s]+`),
	}
}

func (s *PdfChunkingService) ChunkPDF(ctx context.Context, fileBytes []byte, fileID string) ([]domain.Chunk, error) {
	// Context cancellation check
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	// Validate input
	if len(fileBytes) == 0 {
		return nil, ErrEmptyPDF
	}

	// Create PDF reader
	reader := bytes.NewReader(fileBytes)
	pdfReader, err := pdf.NewReader(reader, int64(len(fileBytes)))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrCorruptedPDF, err)
	}

	numPages := pdfReader.NumPage()
	if numPages == 0 {
		return nil, ErrEmptyPDF
	}

	// Extract text with quality tracking
	extractedData, metrics := s.extractTextWithQualityTracking(pdfReader, numPages)

	if len(extractedData.fullText) == 0 {
		return nil, fmt.Errorf("%w: %s", ErrInsufficientText, s.formatQualityReport(metrics))
	}

	// Comprehensive quality validation
	if err := s.validatePDFQuality(extractedData.fullText, metrics); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrLowQualityPDF, err)
	}

	// Semantic chunking with structure preservation
	chunks := s.semanticChunking(extractedData.fullText, extractedData.pageRanges, fileID)

	if len(chunks) == 0 {
		return nil, errors.New("chunk oluşturulamadı: text segmentation hatası")
	}

	// Post-process for quality enhancement
	chunks = s.postProcessChunks(chunks)

	// Final chunk quality validation
	metrics.AvgChunkQuality = s.calculateAverageChunkQuality(chunks)
	if metrics.AvgChunkQuality < 0.3 {
		return nil, fmt.Errorf("%w: chunk kalitesi çok düşük (%.2f/1.0)", ErrLowQualityPDF, metrics.AvgChunkQuality)
	}

	return chunks, nil
}

func (s *PdfChunkingService) ChunkText(ctx context.Context, text string, fileID string) ([]domain.Chunk, error) {
	// Context cancellation check
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	if len(text) == 0 {
		return nil, ErrEmptyPDF
	}

	cleanedText := s.enhancedCleanText(text)

	if len(cleanedText) == 0 {
		return nil, ErrInsufficientText
	}

	// For simple text, we don't have page numbers, so we create a dummy page range
	dummyPageRanges := []pageRange{
		{pageNum: 1, start: 0, end: len(cleanedText)},
	}

	chunks := s.semanticChunking(cleanedText, dummyPageRanges, fileID)

	if len(chunks) == 0 {
		return nil, errors.New("chunk oluşturulamadı: text segmentation hatası")
	}

	chunks = s.postProcessChunks(chunks)

	return chunks, nil
}

type extractedData struct {
	fullText   string
	pageRanges []pageRange
}

type pageRange struct {
	pageNum int
	start   int
	end     int
}

func (s *PdfChunkingService) extractTextWithQualityTracking(pdfReader *pdf.Reader, numPages int) (*extractedData, *QualityMetrics) {
	var fullText strings.Builder
	pageRanges := make([]pageRange, 0, numPages)

	metrics := &QualityMetrics{
		TotalPages:    numPages,
		FailedPages:   make([]int, 0),
		QualityIssues: make([]string, 0),
	}

	currentPos := 0
	successfulPages := 0

	for pageNum := 1; pageNum <= numPages; pageNum++ {
		page := pdfReader.Page(pageNum)
		if page.V.IsNull() {
			metrics.FailedPages = append(metrics.FailedPages, pageNum)
			metrics.QualityIssues = append(metrics.QualityIssues,
				fmt.Sprintf("Sayfa %d: null page object", pageNum))
			continue
		}

		text, err := page.GetPlainText(nil)
		if err != nil {
			metrics.FailedPages = append(metrics.FailedPages, pageNum)
			metrics.QualityIssues = append(metrics.QualityIssues,
				fmt.Sprintf("Sayfa %d: text extraction error", pageNum))
			continue
		}

		// Enhanced cleaning with structure preservation
		cleanedText := s.enhancedCleanText(text)

		// Page quality check
		if utf8.RuneCountInString(cleanedText) < 10 {
			metrics.FailedPages = append(metrics.FailedPages, pageNum)
			metrics.QualityIssues = append(metrics.QualityIssues,
				fmt.Sprintf("Sayfa %d: insufficient text (<10 chars)", pageNum))
			continue
		}

		// Track successful extraction
		successfulPages++
		startPos := currentPos
		fullText.WriteString(cleanedText)
		fullText.WriteString("\n\n")
		currentPos = fullText.Len()

		pageRanges = append(pageRanges, pageRange{
			pageNum: pageNum,
			start:   startPos,
			end:     currentPos,
		})

		// Update metrics
		metrics.TotalChars += utf8.RuneCountInString(cleanedText)
		metrics.TotalWords += len(strings.Fields(cleanedText))
	}

	metrics.ExtractedPages = successfulPages

	// Calculate comprehensive metrics
	if metrics.TotalChars > 0 {
		text := fullText.String()
		metrics.LetterRatio = s.calculateLetterRatio(text)
		metrics.GarbageRatio = s.calculateGarbageRatio(text)
		metrics.PunctuationRatio = s.calculatePunctuationRatio(text)

		if metrics.TotalWords > 0 {
			metrics.AverageWordLength = float64(metrics.TotalChars) / float64(metrics.TotalWords)
		}
	}

	// Quality determination
	metrics.IsHighQuality = s.determineOverallQuality(metrics)

	return &extractedData{
		fullText:   fullText.String(),
		pageRanges: pageRanges,
	}, metrics
}

func (s *PdfChunkingService) enhancedCleanText(text string) string {
	// Step 0: Fix invalid UTF-8 - CRITICAL for gRPC/Qdrant
	text = s.sanitizeUTF8(text)

	// Step 1: Fix hyphenation at line breaks
	lines := strings.Split(text, "\n")
	var cleaned []string

	for i, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		// Merge hyphenated words across lines
		if len(cleaned) > 0 && strings.HasSuffix(cleaned[len(cleaned)-1], "-") {
			// Check if it's a real hyphenation (not bullet point or dash)
			if i > 0 && len(line) > 0 && unicode.IsLetter(rune(line[0])) {
				cleaned[len(cleaned)-1] = strings.TrimSuffix(cleaned[len(cleaned)-1], "-") + line
				continue
			}
		}

		cleaned = append(cleaned, line)
	}

	text = strings.Join(cleaned, " ")

	// Step 2: Normalize unicode spaces
	text = strings.ReplaceAll(text, "\u00A0", " ") // Non-breaking space
	text = strings.ReplaceAll(text, "\u2003", " ") // Em space
	text = strings.ReplaceAll(text, "\u2009", " ") // Thin space
	text = strings.ReplaceAll(text, "\u202F", " ") // Narrow no-break space
	text = strings.ReplaceAll(text, "\u200B", "")  // Zero-width space
	text = strings.ReplaceAll(text, "\u00AD", "")  // Soft hyphen

	// Step 3: Remove control characters and invalid UTF-8
	var result strings.Builder
	for _, r := range text {
		// Skip replacement characters and control chars
		if r == '\uFFFD' || r == '\x00' {
			continue
		}
		// Keep printable chars and essential whitespace
		if unicode.IsPrint(r) || r == '\n' || r == '\t' || r == ' ' {
			// Additional check: skip private use area characters
			if r >= 0xE000 && r <= 0xF8FF {
				continue
			}
			result.WriteRune(r)
		}
	}
	text = result.String()

	// Step 4: Normalize whitespace
	text = regexp.MustCompile(`[ \t]+`).ReplaceAllString(text, " ")
	text = regexp.MustCompile(`\n{3,}`).ReplaceAllString(text, "\n\n")

	// Step 5: Fix common PDF extraction artifacts
	text = regexp.MustCompile(`(\w)-\s+(\w)`).ReplaceAllString(text, "$1$2") // word- break -> wordbreak
	text = regexp.MustCompile(`\s+([.,;:!?])`).ReplaceAllString(text, "$1")  // space before punctuation

	// Step 6: Final UTF-8 validation
	text = s.ensureValidUTF8(text)

	return strings.TrimSpace(text)
}

// sanitizeUTF8 fixes invalid UTF-8 sequences in the text
func (s *PdfChunkingService) sanitizeUTF8(text string) string {
	if utf8.ValidString(text) {
		return text
	}

	// Convert to runes (automatically handles invalid UTF-8)
	runes := []rune(text)

	// Filter out invalid runes
	var valid []rune
	for _, r := range runes {
		// Skip invalid runes and replacement characters
		if r == utf8.RuneError {
			continue
		}
		// Skip null bytes
		if r == 0 {
			continue
		}
		// Skip non-characters (U+FDD0 to U+FDEF)
		if r >= 0xFDD0 && r <= 0xFDEF {
			continue
		}
		// Skip surrogates (U+D800 to U+DFFF)
		if r >= 0xD800 && r <= 0xDFFF {
			continue
		}
		valid = append(valid, r)
	}

	return string(valid)
}

// ensureValidUTF8 is a final safety check before returning text
func (s *PdfChunkingService) ensureValidUTF8(text string) string {
	if !utf8.ValidString(text) {
		// Last resort: convert invalid sequences to spaces
		return strings.ToValidUTF8(text, " ")
	}
	return text
}

func (s *PdfChunkingService) semanticChunking(text string, pageRanges []pageRange, fileID string) []domain.Chunk {
	var chunks []domain.Chunk

	// Detect document structure (sections, paragraphs, lists)
	sections := s.detectSections(text)

	var currentChunk strings.Builder
	var currentStartPos int
	chunkIndex := 0
	textPos := 0

	for _, section := range sections {
		sectionText := section.text
		sectionLen := len(sectionText)

		// Handle oversized sections
		if sectionLen > s.Config.MaxChunkSize {
			// Save current chunk if exists
			if currentChunk.Len() > 0 {
				chunks = s.finalizeChunk(chunks, &currentChunk, fileID, chunkIndex,
					currentStartPos, textPos, pageRanges)
				chunkIndex++
				currentChunk.Reset()
				currentStartPos = textPos
			}

			// Split large section intelligently
			sentences := s.intelligentSentenceSplit(sectionText)
			subChunks := s.chunkSentences(sentences, fileID, chunkIndex, textPos, pageRanges)
			chunks = append(chunks, subChunks...)
			chunkIndex += len(subChunks)
			textPos += sectionLen
			currentStartPos = textPos
			continue
		}

		// Check if adding section exceeds max size
		if currentChunk.Len()+sectionLen+2 > s.Config.MaxChunkSize && currentChunk.Len() > 0 {
			chunks = s.finalizeChunk(chunks, &currentChunk, fileID, chunkIndex,
				currentStartPos, textPos, pageRanges)
			chunkIndex++

			// Smart overlap for context continuity
			if s.Config.OverlapSize > 0 {
				overlapText := s.smartOverlap(currentChunk.String())
				currentChunk.Reset()
				if overlapText != "" {
					currentChunk.WriteString(overlapText)
					currentChunk.WriteString("\n\n")
				}
			} else {
				currentChunk.Reset()
			}

			currentStartPos = textPos
		}

		// Add section with appropriate separator
		currentChunk.WriteString(sectionText)
		if section.isHeader || s.Config.PreserveContext {
			currentChunk.WriteString("\n\n")
		} else {
			currentChunk.WriteString(" ")
		}
		textPos += sectionLen
	}

	// Finalize last chunk
	if currentChunk.Len() > 0 {
		chunks = s.finalizeChunk(chunks, &currentChunk, fileID, chunkIndex,
			currentStartPos, textPos, pageRanges)
	}

	return chunks
}

type section struct {
	text     string
	isHeader bool
	level    int
	isList   bool
}

func (s *PdfChunkingService) detectSections(text string) []section {
	var sections []section
	paragraphs := strings.Split(text, "\n\n")

	for _, para := range paragraphs {
		para = strings.TrimSpace(para)
		if para == "" {
			continue
		}

		// Check if it's a section header
		isHeader := s.sectionHeaders.MatchString(para)
		isList := s.listMarkers.MatchString(para)
		level := 0

		if isHeader {
			// Determine header level
			if len(para) < 100 && strings.ToUpper(para) == para {
				level = 1 // Main header
			} else if strings.HasPrefix(para, "1.") || strings.HasPrefix(para, "I.") {
				level = 2 // Numbered section
			} else {
				level = 3 // Subsection
			}
		}

		sections = append(sections, section{
			text:     para,
			isHeader: isHeader,
			level:    level,
			isList:   isList,
		})
	}

	return sections
}

func (s *PdfChunkingService) intelligentSentenceSplit(text string) []string {
	text = strings.TrimSpace(text)
	if text == "" {
		return []string{}
	}

	var sentences []string
	var current strings.Builder
	runes := []rune(text)
	runeLen := len(runes)

	if runeLen == 0 {
		return []string{}
	}

	for i := 0; i < runeLen; i++ {
		r := runes[i]
		current.WriteRune(r)

		// Sentence boundary detection
		if r == '.' || r == '!' || r == '?' {
			if i >= runeLen-1 {
				break
			}

			currentText := current.String()

			// Abbreviation checks
			if r == '.' {
				words := strings.Fields(currentText)
				if len(words) > 0 {
					lastWord := strings.ToLower(strings.Trim(words[len(words)-1], ".,!?"))

					if len(lastWord) == 0 {
						continue
					}

					// Known abbreviation
					if s.abbreviations[lastWord] {
						continue
					}

					// Decimal number (e.g., 3.14, 1.5)
					if i > 0 && i < runeLen-1 &&
						unicode.IsDigit(runes[i-1]) && unicode.IsDigit(runes[i+1]) {
						continue
					}

					// Initials (e.g., "J.K.")
					if len(lastWord) <= 2 && unicode.IsUpper(rune(lastWord[0])) {
						continue
					}

					// URL or email pattern
					if strings.Contains(currentText, "http") || strings.Contains(currentText, "@") {
						continue
					}
				}
			}

			// Look ahead for sentence boundary
			if i+1 < runeLen {
				// Skip whitespace
				j := i + 1
				for j < runeLen && unicode.IsSpace(runes[j]) {
					j++
				}

				// Verify new sentence starts
				if j < runeLen {
					nextChar := runes[j]
					if unicode.IsUpper(nextChar) || unicode.IsDigit(nextChar) ||
						nextChar == '"' || nextChar == '(' || nextChar == '[' {
						sentText := strings.TrimSpace(current.String())
						if sentText != "" {
							sentences = append(sentences, sentText)
						}
						current.Reset()
						i = j - 1 // Skip whitespace
						continue
					}
				}
			}
		}
	}

	// Add remaining text
	if current.Len() > 0 {
		sentText := strings.TrimSpace(current.String())
		if sentText != "" {
			sentences = append(sentences, sentText)
		}
	}

	// Fallback
	if len(sentences) == 0 && text != "" {
		return []string{text}
	}

	return sentences
}

func (s *PdfChunkingService) chunkSentences(sentences []string, fileID string,
	startChunkIndex, startPos int, pageRanges []pageRange) []domain.Chunk {

	var chunks []domain.Chunk
	var current strings.Builder
	chunkIndex := startChunkIndex
	currentStartPos := startPos
	pos := startPos

	for _, sent := range sentences {
		sentLen := len(sent)

		if current.Len()+sentLen+1 > s.Config.MaxChunkSize && current.Len() > 0 {
			chunks = s.finalizeChunk(chunks, &current, fileID, chunkIndex,
				currentStartPos, pos, pageRanges)
			chunkIndex++

			// Add overlap
			if s.Config.OverlapSize > 0 {
				overlapText := s.smartOverlap(current.String())
				current.Reset()
				if overlapText != "" {
					current.WriteString(overlapText)
					current.WriteString(" ")
				}
			} else {
				current.Reset()
			}

			currentStartPos = pos
		}

		current.WriteString(sent)
		current.WriteString(" ")
		pos += sentLen
	}

	if current.Len() > 0 {
		chunks = s.finalizeChunk(chunks, &current, fileID, chunkIndex,
			currentStartPos, pos, pageRanges)
	}

	return chunks
}

func (s *PdfChunkingService) smartOverlap(text string) string {
	text = strings.TrimSpace(text)
	if len(text) <= s.Config.OverlapSize {
		return text
	}

	// Get last N characters
	runes := []rune(text)
	startIdx := len(runes) - s.Config.OverlapSize
	if startIdx < 0 {
		startIdx = 0
	}

	overlapText := string(runes[startIdx:])

	// Try to start from sentence boundary
	sentenceMarkers := []rune{'.', '!', '?'}
	for i, r := range []rune(overlapText) {
		for _, marker := range sentenceMarkers {
			if r == marker && i < len(overlapText)-1 {
				remaining := overlapText[i+1:]
				remaining = strings.TrimSpace(remaining)
				if len(remaining) > 20 { // Minimum viable overlap
					return remaining
				}
			}
		}
	}

	// Fallback: word boundary
	words := strings.Fields(overlapText)
	if len(words) > 2 {
		return strings.Join(words[1:], " ")
	}

	return strings.TrimSpace(overlapText)
}

func (s *PdfChunkingService) finalizeChunk(chunks []domain.Chunk, current *strings.Builder,
	fileID string, chunkIndex, startPos, endPos int, pageRanges []pageRange) []domain.Chunk {

	text := strings.TrimSpace(current.String())
	if text == "" {
		return chunks
	}

	// CRITICAL: Ensure valid UTF-8 before finalizing
	text = s.ensureValidUTF8(text)

	// Final validation
	if !utf8.ValidString(text) {
		// If still invalid after cleaning, skip this chunk
		return chunks
	}

	chunk := domain.Chunk{
		Text: text,
		Metadata: domain.ChunkMetadata{
			FileID:      fileID,
			ChunkIndex:  chunkIndex,
			PageNumbers: getPagesForRange(startPos, endPos, pageRanges),
			CharCount:   utf8.RuneCountInString(text),
		},
	}

	return append(chunks, chunk)
}

func (s *PdfChunkingService) postProcessChunks(chunks []domain.Chunk) []domain.Chunk {
	var processed []domain.Chunk

	for i, chunk := range chunks {
		text := chunk.Text

		// CRITICAL: Validate UTF-8 before any processing
		if !utf8.ValidString(text) {
			text = s.ensureValidUTF8(text)
		}

		// Skip very short chunks (merge with next if possible)
		if utf8.RuneCountInString(text) < 50 && i < len(chunks)-1 {
			if i+1 < len(chunks) {
				nextChunk := chunks[i+1]
				merged := text + " " + nextChunk.Text

				if len(merged) <= s.Config.MaxChunkSize {
					// Validate merged text
					merged = s.ensureValidUTF8(merged)

					chunks[i+1].Text = merged
					chunks[i+1].Metadata.PageNumbers = uniquePages(
						chunk.Metadata.PageNumbers,
						nextChunk.Metadata.PageNumbers,
					)
					chunks[i+1].Metadata.CharCount = utf8.RuneCountInString(merged)
					continue
				}
			}
		}

		// Normalize formatting
		text = normalizeWhitespace(text)

		// Final UTF-8 validation before adding to processed chunks
		text = s.ensureValidUTF8(text)

		// Double-check: If still invalid, skip this chunk
		if !utf8.ValidString(text) {
			continue
		}

		chunk.Text = text
		chunk.Metadata.CharCount = utf8.RuneCountInString(text)

		processed = append(processed, chunk)
	}

	return processed
}

func (s *PdfChunkingService) validatePDFQuality(fullText string, metrics *QualityMetrics) error {
	textLen := utf8.RuneCountInString(fullText)

	// Check 1: Minimum text length
	if textLen < 100 {
		return fmt.Errorf("yetersiz metin içeriği (%d karakter, minimum 100 gerekli)", textLen)
	}

	// Check 2: Letter ratio
	if metrics.LetterRatio < 0.5 {
		return fmt.Errorf("okunamaz metin (%.1f%% harf, minimum 50%% gerekli) - taranmış veya bozuk PDF olabilir",
			metrics.LetterRatio*100)
	}

	// Check 3: Word count and coherence
	words := strings.Fields(fullText)
	if len(words) < 20 {
		return fmt.Errorf("yetersiz kelime sayısı (%d kelime, minimum 20 gerekli)", len(words))
	}

	// Check 4: Average word length
	if metrics.AverageWordLength < 2.5 || metrics.AverageWordLength > 15 {
		return fmt.Errorf("anormal kelime yapısı (ortalama %.1f karakter) - taranmış veya bozuk PDF olabilir",
			metrics.AverageWordLength)
	}

	// Check 5: Sentence structure
	wordsPerPunctuation := float64(len(words)) / (metrics.PunctuationRatio*float64(textLen) + 1)
	if wordsPerPunctuation > 100 {
		return fmt.Errorf("cümle yapısı eksik (%.0f kelime/noktalama) - taranmış veya bozuk PDF olabilir",
			wordsPerPunctuation)
	}

	// Check 6: Garbage ratio
	if metrics.GarbageRatio > 0.05 {
		return fmt.Errorf("yüksek anlamsız karakter oranı (%.1f%%) - bozuk veya kötü taranmış PDF",
			metrics.GarbageRatio*100)
	}

	// Check 7: Page extraction rate
	extractionRate := float64(metrics.ExtractedPages) / float64(metrics.TotalPages)
	if extractionRate < 0.5 {
		return fmt.Errorf("düşük sayfa çıkarma oranı (%.1f%%, %d/%d sayfa) - PDF bozuk olabilir",
			extractionRate*100, metrics.ExtractedPages, metrics.TotalPages)
	}

	// Check 8: Language coherence
	if !s.hasLanguageCoherence(fullText) {
		return errors.New("tanınabilir dil kalıbı bulunamadı - taranmış, şifreli veya desteklenmeyen dil")
	}

	return nil
}

func (s *PdfChunkingService) calculateLetterRatio(text string) float64 {
	if len(text) == 0 {
		return 0
	}

	letterCount := 0
	for _, r := range text {
		if unicode.IsLetter(r) {
			letterCount++
		}
	}

	return float64(letterCount) / float64(utf8.RuneCountInString(text))
}

func (s *PdfChunkingService) calculateGarbageRatio(text string) float64 {
	if len(text) == 0 {
		return 1.0
	}

	garbageCount := 0
	for _, r := range text {
		// Garbage characters from poor OCR/encoding
		if r == '�' || r == '\uFFFD' ||
			(r >= 0x2400 && r <= 0x243F) ||
			(r >= 0xE000 && r <= 0xF8FF) {
			garbageCount++
		}

		// Invalid characters
		if !unicode.IsLetter(r) && !unicode.IsNumber(r) &&
			!unicode.IsSpace(r) && !unicode.IsPunct(r) {
			garbageCount++
		}
	}

	return float64(garbageCount) / float64(utf8.RuneCountInString(text))
}

func (s *PdfChunkingService) calculatePunctuationRatio(text string) float64 {
	if len(text) == 0 {
		return 0
	}

	punctCount := 0
	for _, r := range text {
		if unicode.IsPunct(r) {
			punctCount++
		}
	}

	return float64(punctCount) / float64(utf8.RuneCountInString(text))
}

func (s *PdfChunkingService) hasLanguageCoherence(text string) bool {
	sampleLen := min(5000, len(text))
	lowerText := strings.ToLower(text[:sampleLen])

	// Common words in major languages
	commonWords := []string{
		// English
		"the", "and", "is", "in", "to", "of", "a", "for", "that", "with",
		// Turkish
		"ve", "bir", "bu", "için", "ile", "de", "da", "en", "var", "olan",
		// German
		"der", "die", "das", "und", "den", "ist", "zu", "ein",
		// French
		"le", "de", "la", "et", "un", "une", "est", "dans",
		// Spanish
		"el", "la", "de", "que", "y", "en", "los", "del",
	}

	matchCount := 0
	for _, word := range commonWords {
		if strings.Contains(lowerText, " "+word+" ") {
			matchCount++
		}
	}

	return matchCount >= 3
}

func (s *PdfChunkingService) calculateChunkQuality(text string) float64 {
	score := 0.0
	runeCount := utf8.RuneCountInString(text)

	if runeCount == 0 {
		return 0.0
	}

	// 1. Length score (optimal: 400-800 chars)
	optimalLength := 600.0
	lengthScore := 1.0 - math.Abs(float64(runeCount)-optimalLength)/optimalLength
	if lengthScore < 0 {
		lengthScore = 0
	}
	score += lengthScore * 0.25

	// 2. Sentence completeness
	trimmed := strings.TrimSpace(text)
	if strings.HasSuffix(trimmed, ".") ||
		strings.HasSuffix(trimmed, "!") ||
		strings.HasSuffix(trimmed, "?") {
		score += 0.2
	}

	// 3. Word diversity
	words := strings.Fields(strings.ToLower(text))
	if len(words) > 0 {
		uniqueWords := make(map[string]bool)
		for _, w := range words {
			uniqueWords[w] = true
		}
		diversity := float64(len(uniqueWords)) / float64(len(words))
		score += diversity * 0.25
	}

	// 4. Letter content ratio
	letterCount := 0
	for _, r := range text {
		if unicode.IsLetter(r) {
			letterCount++
		}
	}
	if runeCount > 0 {
		alphaRatio := float64(letterCount) / float64(runeCount)
		score += alphaRatio * 0.2
	}

	// 5. Structure indicators (headers, lists, citations)
	if s.sectionHeaders.MatchString(text) {
		score += 0.05
	}
	if s.listMarkers.MatchString(text) {
		score += 0.05
	}

	return score
}

func (s *PdfChunkingService) calculateAverageChunkQuality(chunks []domain.Chunk) float64 {
	if len(chunks) == 0 {
		return 0.0
	}

	totalQuality := 0.0
	for _, chunk := range chunks {
		totalQuality += s.calculateChunkQuality(chunk.Text)
	}

	return totalQuality / float64(len(chunks))
}

func (s *PdfChunkingService) determineOverallQuality(metrics *QualityMetrics) bool {
	// Multiple quality checks
	checks := []bool{
		metrics.LetterRatio >= 0.5,
		metrics.AverageWordLength >= 2.5 && metrics.AverageWordLength <= 15,
		metrics.GarbageRatio <= 0.05,
		metrics.PunctuationRatio >= 0.01 && metrics.PunctuationRatio <= 0.15,
		float64(metrics.ExtractedPages)/float64(metrics.TotalPages) >= 0.5,
		metrics.TotalWords >= 20,
	}

	passedChecks := 0
	for _, check := range checks {
		if check {
			passedChecks++
		}
	}

	// Need at least 5 out of 6 checks to pass
	return passedChecks >= 5
}

func (s *PdfChunkingService) formatQualityReport(metrics *QualityMetrics) string {
	var report strings.Builder

	report.WriteString(fmt.Sprintf("Toplam sayfa: %d, ", metrics.TotalPages))
	report.WriteString(fmt.Sprintf("Çıkarılan: %d (%.1f%%), ",
		metrics.ExtractedPages,
		float64(metrics.ExtractedPages)/float64(metrics.TotalPages)*100))

	if metrics.ExtractedPages > 0 {
		avgChars := float64(metrics.TotalChars) / float64(metrics.ExtractedPages)
		avgWords := float64(metrics.TotalWords) / float64(metrics.ExtractedPages)
		report.WriteString(fmt.Sprintf("Sayfa başına: %.0f karakter / %.0f kelime, ",
			avgChars, avgWords))
	}

	report.WriteString(fmt.Sprintf("Harf oranı: %.1f%%, ", metrics.LetterRatio*100))
	report.WriteString(fmt.Sprintf("Ort. kelime: %.1f karakter, ", metrics.AverageWordLength))
	report.WriteString(fmt.Sprintf("Anlamsız: %.1f%%", metrics.GarbageRatio*100))

	if len(metrics.QualityIssues) > 0 {
		report.WriteString("\nSorunlar: ")
		report.WriteString(strings.Join(metrics.QualityIssues[:min(3, len(metrics.QualityIssues))], "; "))
	}

	return report.String()
}

func getPagesForRange(start, end int, pageRanges []pageRange) []int {
	pageSet := make(map[int]bool)
	for _, pr := range pageRanges {
		if !(end <= pr.start || start >= pr.end) {
			pageSet[pr.pageNum] = true
		}
	}

	pages := make([]int, 0, len(pageSet))
	for page := range pageSet {
		pages = append(pages, page)
	}
	sort.Ints(pages)
	return pages
}

func normalizeWhitespace(text string) string {
	// Multiple spaces -> single space
	text = regexp.MustCompile(`[ \t]+`).ReplaceAllString(text, " ")
	// Multiple newlines -> double newline
	text = regexp.MustCompile(`\n{3,}`).ReplaceAllString(text, "\n\n")
	return strings.TrimSpace(text)
}

func uniquePages(pages1, pages2 []int) []int {
	pageSet := make(map[int]bool)
	for _, p := range pages1 {
		pageSet[p] = true
	}
	for _, p := range pages2 {
		pageSet[p] = true
	}

	result := make([]int, 0, len(pageSet))
	for p := range pageSet {
		result = append(result, p)
	}
	sort.Ints(result)
	return result
}

func (s *PdfChunkingService) GetChunks(ctx context.Context, fileId uuid.UUID) ([]string, error) {
	// Not fully implemented for quiz yet, since chunks are handled differently in DB in real scenario.
	// But to satisfy interface, we will return some dummy or we can implement real logic.
	return nil, errors.New("Not Implemented")
}

func (s *PdfChunkingService) GetChunksByPages(ctx context.Context, fileId uuid.UUID, pageRange []int) ([]string, error) {
	return nil, errors.New("Not Implemented")
}
