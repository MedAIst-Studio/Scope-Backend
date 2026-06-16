package services

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"github.com/MedAIst-Studio/MedAIist-Backend/domain"
	"github.com/google/uuid"
)

// ---------- AI System Prompts ----------

const (
	generalChatSystemMessage = `Sen yardımsever ve samimi bir yapay zeka asistanısın. Kullanıcının her türlü sorusuna bilgilendirici ve doğal yanıtlar ver.
Cevaplarını Türkçe ver. Gerektiğinde Markdown formatını kullan.`

	imageQuerySystemMessage = "Sen bir soru çözüm asistanısın. Gönderilen görseldeki soruyu analiz et ve çözümünü ver. Eğer görsel dersle/sınavla ilgili değilse, nazikçe cevap veremeyeceğini belirt."

	pdfSearchQuerySystemMessage = `Sen Uzman bir YKS Eğitmenisin. Soruları SADECE aşağıda verilen Bağlam (Context) içeriğini kullanarak cevapla.

KRİTİK KURALLAR:
1. **Cevap Kontrolü:**
   - Eğer Bağlam içinde soruyla ilgili HERHANGİ bir bilgi varsa, mutlaka kullan.
   - SADECE ve SADECE bağlamda HİÇBİR bilgi yoksa "Yüklediğiniz dokümanda bu bilgi yer almamaktadır" de.
2. **Cevap Oluşturma:**
   - Önemli terimler için **Kalın** font kullan.
   - Listeler için madde işaretleri kullan.
   - Bağlam/Doküman içindeki bilgileri kullanarak kapsamlı ve doyurucu bir cevap ver.
   - Mümkünse dokümandan alıntı yap veya oradaki bilgiyi yorumla.
3. **Dil:**
   - Türkçe cevapla.
4. **Önemli:**
   - Konuyla dolaylı ilgili bilgi varsa ekle.
`
)

// ---------- Chat Request Manager ----------

type chatRequestManager struct {
	QueryType   domain.QueryType
	UserContent string
	Req         *domain.ChatRequest
}

func newChatRequestManager(queryType domain.QueryType, userContent string) *chatRequestManager {
	return &chatRequestManager{
		QueryType:   queryType,
		UserContent: userContent,
		Req:         newChatRequest(queryType, userContent),
	}
}

func newChatMessage(role domain.Role, content string) domain.ChatMessage {
	return domain.ChatMessage{
		Role:    role,
		Content: content,
	}
}

func newChatRequest(queryType domain.QueryType, userContent string) *domain.ChatRequest {
	systemMessage := getSystemMessageForQueryType(queryType)

	return &domain.ChatRequest{
		Messages: []domain.ChatMessage{
			newChatMessage(domain.RoleSystem, systemMessage),
			newChatMessage(domain.RoleUser, userContent),
		},
	}
}

func getSystemMessageForQueryType(queryType domain.QueryType) string {
	switch queryType {
	case domain.PdfSearchQuery:
		return pdfSearchQuerySystemMessage
	case domain.ImageQuery:
		return imageQuerySystemMessage
	case domain.GeneralChatQuery:
		return generalChatSystemMessage
	default:
		return generalChatSystemMessage
	}
}

func newChatTitleRequest(firstUserMessage string) *domain.ChatRequest {
	return &domain.ChatRequest{
		Stream: false,
		Messages: []domain.ChatMessage{
			newChatMessage(
				domain.RoleSystem,
				"Generate a 3-7 word title for this chat in turkish: "+firstUserMessage,
			),
		},
	}
}

func (crm *chatRequestManager) isPdfSearchQuery() bool {
	return crm.QueryType == domain.PdfSearchQuery
}

type MessageService struct {
	PastMessagesLimit int
	Stream            bool
	Repo              domain.MessageRepository
	Vb                domain.VectorDB
	Ems               domain.SingleEmbeddingService
	ChatSvc           domain.AIService
	FileService       domain.FileStorageService
	StreakSvc         domain.StreakService
}

func NewMessageService(
	pastMessagesLimit int,
	stream bool,
	repo domain.MessageRepository,
	vb domain.VectorDB,
	ems domain.SingleEmbeddingService,
	chatSvc domain.AIService,
	fileService domain.FileStorageService,
	streakSvc domain.StreakService,
) *MessageService {
	return &MessageService{
		PastMessagesLimit: pastMessagesLimit,
		Stream:            stream,
		Repo:              repo,
		Vb:                vb,
		Ems:               ems,
		ChatSvc:           chatSvc,
		FileService:       fileService,
		StreakSvc:         streakSvc,
	}
}

func (s *MessageService) SendMessage(ctx context.Context, userId uuid.UUID, req *domain.SendMessageRequest) (*domain.SendMessageResponse, error) {
	userMessage := domain.NewMessage(req.ChatId, req.Content, true)

	processImage := func(url string) error {
		userMessage.Attachments = append(userMessage.Attachments, domain.Attachment{
			Type: "image",
			Url:  url,
		})
		return nil
	}

	if req.ImageUrl != "" {
		processImage(req.ImageUrl)
	}

	isNewChat := req.ChatId == uuid.Nil

	chatTitle, err := s.handleNewChatIfNeeded(ctx, req, userId, userMessage, isNewChat)
	if err != nil {
		return nil, err
	}

	var queryType domain.QueryType

	if req.PdfId != uuid.Nil && req.ImageUrl == "" {
		fileMeta, err := s.Repo.GetFileMetadata(ctx, req.PdfId)
		if err == nil {
			if fileMeta.FileType == domain.FileTypeImage {
				url := "uploads/" + req.PdfId.String()
				req.ImageUrl = url
				processImage(url)
			} else if fileMeta.FileType == domain.FileTypeYouTube {
				userMessage.ContextSnapshot = &domain.ContextSnapshot{
					Type:        "youtube",
					DocumentId:  req.PdfId.String(),
					CachedTitle: fileMeta.Name,
				}
			} else {
				userMessage.ContextSnapshot = &domain.ContextSnapshot{
					Type:        "pdf",
					DocumentId:  req.PdfId.String(),
					CachedTitle: fileMeta.Name,
				}
			}
		}
	}

	isGeneralChat := req.PdfId == uuid.Nil && req.ImageUrl == ""

	if isGeneralChat {
		queryType = domain.GeneralChatQuery
	} else if req.ImageUrl != "" {
		queryType = domain.ImageQuery
	} else {
		queryType = domain.PdfSearchQuery
	}

	if err := s.Repo.SaveMessage(ctx, userMessage); err != nil {
		return nil, err
	}

	crm := newChatRequestManager(queryType, req.Content)

	if queryType == domain.ImageQuery && len(userMessage.Attachments) > 0 {
		if len(crm.Req.Messages) > 0 {
			lastMsgIdx := len(crm.Req.Messages) - 1
			if crm.Req.Messages[lastMsgIdx].Role == domain.RoleUser {
				if len(userMessage.Attachments) > 0 {
					crm.Req.Messages[lastMsgIdx].ImageUrl = []string{userMessage.Attachments[0].Url}
				}
			}
		}
	}

	if err := s.processQuery(ctx, req, userMessage, crm, isNewChat); err != nil {
		if errors.Is(err, domain.ErrInfoNotFoundInPDF) {
			return s.handleInfoNotFound(ctx, userMessage, chatTitle, isNewChat, err)
		}
		return nil, err
	}

	if queryType == domain.ImageQuery {
		var attachment *domain.Attachment
		for i := range userMessage.Attachments {
			if strings.Contains(userMessage.Attachments[i].Url, "uploads/") {
				attachment = &userMessage.Attachments[i]
				break
			}
		}

		if attachment != nil {
			var fileBytes []byte
			var err error

			// Parse path
			idx := strings.Index(attachment.Url, "uploads/")
			if idx != -1 {
				objectPath := attachment.Url[idx:]
				fileBytes, err = s.FileService.GetFile(ctx, objectPath)
				if err != nil {
					return nil, fmt.Errorf("failed to get file bytes from path %s: %w", objectPath, err)
				}
			}

			base64Image := convertImageToBase64(fileBytes)

			extractedText, err := s.ChatSvc.ExtractTextFromImage(ctx, base64Image)
			if err != nil {
				return nil, fmt.Errorf("OCR failed: %w", err)
			}

			reasoningAns, err := s.ChatSvc.SolveQuestionWithReasoning(ctx, extractedText)
			if err != nil {
				return nil, fmt.Errorf("reasoning failed: %w", err)
			}

			aiMessage := domain.NewMessage(userMessage.ChatId, reasoningAns, false)
			if err := s.Repo.SaveMessage(ctx, aiMessage); err != nil {
				return nil, err
			}

			s.StreakSvc.CheckAndUpdateStreak(ctx, userId)
			return s.buildResponse(userMessage, aiMessage, chatTitle, isNewChat), nil
		}
	}

	crm.Req.Stream = s.Stream
	aiResp, err := s.ChatSvc.SendChatRequest(ctx, crm.Req)
	if err != nil {
		return nil, err
	}

	aiMessage := domain.NewMessage(userMessage.ChatId, aiResp.Text, false)
	if err := s.Repo.SaveMessage(ctx, aiMessage); err != nil {
		return nil, err
	}

	s.StreakSvc.CheckAndUpdateStreak(ctx, userId)
	return s.buildResponse(userMessage, aiMessage, chatTitle, isNewChat), nil
}

func (s *MessageService) handleNewChatIfNeeded(ctx context.Context, req *domain.SendMessageRequest, userId uuid.UUID, userMessage *domain.Message, isNewChat bool) (string, error) {
	if !isNewChat {
		return "", nil
	}

	aiResp, err := s.ChatSvc.GenerateChatTitle(ctx, newChatTitleRequest(req.Content))
	if err != nil {
		return "", err
	}

	chat := domain.NewChat(userId, trimTitle(aiResp.Text))
	if err := s.Repo.CreateChat(ctx, chat); err != nil {
		return "", err
	}

	userMessage.UpdateChatId(chat.Id)
	return chat.Title, nil
}

func (s *MessageService) processQuery(ctx context.Context, req *domain.SendMessageRequest, userMessage *domain.Message, crm *chatRequestManager, isNewChat bool,
) error {
	if !isNewChat {
		pastMessages, err := s.Repo.GetChatHistoryForAIContext(
			ctx,
			userMessage.ChatId,
			s.PastMessagesLimit,
		)
		if err != nil {
			return err
		}
		appendMessages(crm.Req, pastMessages)
		removeUserMessageToEnd(crm.Req)
	}

	if crm.isPdfSearchQuery() {
		return s.addPdfContext(ctx, req, crm)
	}
	return nil
}

type searchResult struct {
	chunks []string
	err    error
}

func (s *MessageService) addPdfContext(ctx context.Context, req *domain.SendMessageRequest, crm *chatRequestManager) error {
	generatedQueries, err := s.ChatSvc.ExpandQuery(ctx, req.Content)
	if err != nil {
		return err
	}

	results := make(chan searchResult, len(generatedQueries))
	var wg sync.WaitGroup

	for _, query := range generatedQueries {
		wg.Add(1)
		go func(q string) {
			defer wg.Done()

			embedding, err := s.Ems.GenerateEmbedding(ctx, &domain.SingleEmbeddingRequest{Text: q})
			if err != nil {
				results <- searchResult{err: fmt.Errorf("embedding failed for query '%s': %w", q, err)}
				return
			}

			relevantTexts, err := s.Vb.SearchByEmbedding(ctx, req.PdfId, embedding.Vector)
			if err != nil {
				results <- searchResult{err: fmt.Errorf("search failed for query '%s': %w", q, err)}
				return
			}

			results <- searchResult{chunks: relevantTexts}
		}(query)
	}

	go func() {
		wg.Wait()
		close(results)
	}()

	var allChunks []string
	for result := range results {
		if result.err != nil {
			return result.err
		}
		allChunks = append(allChunks, result.chunks...)
	}

	uniqueChunks := deduplicateChunks(allChunks)
	rerankedChunks, err := s.ChatSvc.RerankChunks(ctx, uniqueChunks, req.Content)
	if err != nil {
		return fmt.Errorf("reranking failed: %w", err)
	}
	addRelevantExcerpts(crm.Req, rerankedChunks)
	return nil
}

func (s *MessageService) handleInfoNotFound(ctx context.Context, userMessage *domain.Message, chatTitle string, isNewChat bool, err error) (*domain.SendMessageResponse, error) {
	aiMessage := domain.NewMessage(userMessage.ChatId, err.Error(), false)

	if saveErr := s.Repo.SaveMessage(ctx, aiMessage); saveErr != nil {
		return nil, saveErr
	}

	return s.buildResponse(userMessage, aiMessage, chatTitle, isNewChat), nil
}

func (s *MessageService) buildResponse(userMessage, aiMessage *domain.Message, chatTitle string, isNewChat bool) *domain.SendMessageResponse {
	resp := &domain.SendMessageResponse{
		AiResponse:    aiMessage.Content,
		UserMessageId: userMessage.Id,
		AiMessageId:   aiMessage.Id,
	}

	if isNewChat {
		resp.Title = chatTitle
		resp.ChatId = &userMessage.ChatId
	}

	return resp
}

func trimTitle(s string) string {
	return strings.ReplaceAll(s, "\"", "")
}

func convertImageToBase64(data []byte) string {
	mimeType := http.DetectContentType(data)
	base64Str := base64.StdEncoding.EncodeToString(data)
	return fmt.Sprintf("data:%s;base64,%s", mimeType, base64Str)
}

// ---------- ChatRequest Helpers ----------

func appendMessages(req *domain.ChatRequest, messages []domain.ChatMessage) {
	req.Messages = append(req.Messages, messages...)
}

func removeUserMessageToEnd(req *domain.ChatRequest) {
	if len(req.Messages) < 2 {
		return
	}
	second := req.Messages[1]
	req.Messages = append(req.Messages[:1], req.Messages[2:]...)
	req.Messages = append(req.Messages, second)
}

func addRelevantExcerpts(req *domain.ChatRequest, excerpts []string) {
	if len(excerpts) == 0 {
		return
	}
	for _, excerpt := range excerpts {
		req.Messages = append(req.Messages, newChatMessage(domain.RoleUser, excerpt))
	}
}
