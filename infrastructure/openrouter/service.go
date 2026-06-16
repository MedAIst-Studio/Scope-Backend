package openrouter

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/MedAIst-Studio/MedAIist-Backend/domain"
	"github.com/kkdai/youtube/v2"
)

// Consts
const (
	openRouterBaseURL = "https://openrouter.ai/api/v1"

	queryExpansionSystemPrompt = `Sen bir arama sorgusu genişletme uzmanısın. Görevin, kullanıcının asıl niyetini koruyarak farklı açılardan 5 adet Türkçe arama sorgusu üretmektir.

	Kurallar:
	1. Asıl niyeti koru - kullanıcının ne sorduğunu değiştirme.
	2. Farklı ifadeler, eş anlamlılar ve bakış açıları kullan.
	3. YKS (TYT/AYT) müfredatına uygun terimler kullanmaya çalış.
	4. Asıl sorguda olmayan tamamen yeni bilgi ekleme.

	SADECE şu formatta geçerli bir JSON döndür:
	{"queries": ["...", "...", "...", "...", "..."]}
`
)

// ----------------------------------------------------------------------
// STRUCTS (Native implementation to replace go-openai)
// ----------------------------------------------------------------------

type ChatMessagePartType string

const (
	ChatMessagePartTypeText     ChatMessagePartType = "text"
	ChatMessagePartTypeImageURL ChatMessagePartType = "image_url"
)

type ChatMessageImageURL struct {
	URL string `json:"url,omitempty"`
}

type ChatMessagePart struct {
	Type     ChatMessagePartType  `json:"type,omitempty"`
	Text     string               `json:"text,omitempty"`
	ImageURL *ChatMessageImageURL `json:"image_url,omitempty"`
}

type ChatCompletionMessage struct {
	Role         string            `json:"role"`
	Content      interface{}       `json:"content"` // string or []ChatMessagePart
	MultiContent []ChatMessagePart `json:"-"`       // Helper to build content (not used in JSON marshaling directly, handled by logic)
}

type ResponseFormat struct {
	Type       string                `json:"type,omitempty"` // "json_object" or "json_schema"
	JSONSchema *ResponseFormatSchema `json:"json_schema,omitempty"`
}

type ResponseFormatSchema struct {
	Name   string          `json:"name"`
	Schema json.RawMessage `json:"schema"`
	Strict bool            `json:"strict,omitempty"`
}

type ChatCompletionRequest struct {
	Model          string                  `json:"model"`
	Messages       []ChatCompletionMessage `json:"messages"`
	Temperature    float32                 `json:"temperature,omitempty"`
	Stream         bool                    `json:"stream,omitempty"`
	ResponseFormat *ResponseFormat         `json:"response_format,omitempty"`
}

type ChatCompletionChoice struct {
	Index        int                   `json:"index"`
	Message      ChatCompletionMessage `json:"message"`
	FinishReason string                `json:"finish_reason"`
}

type ChatCompletionResponse struct {
	ID      string                 `json:"id"`
	Object  string                 `json:"object"`
	Created int64                  `json:"created"`
	Model   string                 `json:"model"`
	Choices []ChatCompletionChoice `json:"choices"`
}

type EmbeddingRequest struct {
	Input interface{} `json:"input"` // string or []string
	Model string      `json:"model"`
}

type EmbeddingData struct {
	Object    string    `json:"object"`
	Embedding []float64 `json:"embedding"` // OpenRouter usually returns float64
	Index     int       `json:"index"`
}

type EmbeddingResponse struct {
	Object string          `json:"object"`
	Data   []EmbeddingData `json:"data"`
	Model  string          `json:"model"`
	Usage  interface{}     `json:"usage"`
}

// ----------------------------------------------------------------------
// DYNAMIC PROMPT SYSTEM - Subject-specific quiz generation
// ----------------------------------------------------------------------

type SubjectPromptConfig struct {
	SubjectName       string
	SystemPromptExtra string // Subject-specific additions to the base system prompt
	DistractorRules   string // Subject-specific distractor crafting rules
	FewShotExample    string // One high-quality YKS example per subject
}

var subjectPrompts = map[string]SubjectPromptConfig{
	"biyoloji": {
		SubjectName:       "Biyoloji",
		SystemPromptExtra: `Bu bir BİYOLOJİ sorusudur. Hücre yapıları, organeller, genetik, ekosistem, sınıflandırma, fizyoloji gibi biyoloji alt dallarına hakimsin.`,
		DistractorRules: `- Çeldiricilerde benzer biyolojik terimleri kullan (ör: mitoz/mayoz, DNA/RNA, osmoz/difüzyon)
- Süreç sıralama sorularında adımları karıştır
- Organellerin işlevlerini birbiriyle değiştir
- Bitki/hayvan hücresi karıştırmaları yap`,
		FewShotExample: `ÖRNEK SORU:
{
  "questionText": "Ökaryot hücrelerde protein sentezinin başladığı organel aşağıdakilerden hangisidir?",
  "difficulty": "orta",
  "topic": "Hücre Biyolojisi - Organeller",
  "options": [
    {"no": 1, "label": "A", "text": "Ribozom", "isCorrect": true},
    {"no": 2, "label": "B", "text": "Golgi cisimciği", "isCorrect": false},
    {"no": 3, "label": "C", "text": "Endoplazmik retikulum", "isCorrect": false},
    {"no": 4, "label": "D", "text": "Lizozom", "isCorrect": false},
    {"no": 5, "label": "E", "text": "Mitokondri", "isCorrect": false}
  ],
  "explanation": "Protein sentezi ribozomlarda başlar. Golgi cisimciği proteinleri paketler, ER taşıma sağlar, lizozom sindirim yapar, mitokondri enerji üretir."
}`,
	},
	"matematik": {
		SubjectName:       "Matematik",
		SystemPromptExtra: `Bu bir MATEMATİK sorusudur. Sayılar, cebir, fonksiyonlar, geometri, olasılık, türev-integral gibi matematik alt dallarına hakimsin.`,
		DistractorRules: `- Çeldiricilerde yaygın hesaplama hatalarının sonuçlarını kullan
- İşlem önceliği hatası yapıldığında çıkacak sonuçları şık olarak koy
- Formülü yanlış uygulama sonuçlarını kullan
- Negatif/pozitif işaret hatalarının sonuçlarını ekle`,
		FewShotExample: `ÖRNEK SORU:
{
  "questionText": "f(x) = 2x² - 8x + 6 fonksiyonunun tepe noktasının y koordinatı kaçtır?",
  "difficulty": "orta",
  "topic": "İkinci Derece Fonksiyonlar",
  "options": [
    {"no": 1, "label": "A", "text": "-2", "isCorrect": true},
    {"no": 2, "label": "B", "text": "2", "isCorrect": false},
    {"no": 3, "label": "C", "text": "6", "isCorrect": false},
    {"no": 4, "label": "D", "text": "-6", "isCorrect": false},
    {"no": 5, "label": "E", "text": "0", "isCorrect": false}
  ],
  "explanation": "Tepe noktası x = -b/2a = 8/4 = 2. f(2) = 2(4) - 16 + 6 = -2. Diğer şıklar yaygın işaret ve hesap hatalarıdır."
}`,
	},
	"fizik": {
		SubjectName:       "Fizik",
		SystemPromptExtra: `Bu bir FİZİK sorusudur. Mekanik, termodinamik, elektrik, optik, dalgalar, modern fizik gibi fizik alt dallarına hakimsin.`,
		DistractorRules: `- Çeldiricilerde birim dönüşüm hataları kullan
- Yanlış formül uygulanırsa çıkacak sonuçları ekle
- Vektörel/skaler büyüklük karıştırmalarını kullan
- Newton yasalarının yanlış uygulanması sonuçlarını ekle`,
		FewShotExample: `ÖRNEK SORU:
{
  "questionText": "Kütlesi 2 kg olan bir cisim sürtünmesiz yatay düzlemde 10 N sabit kuvvetle itilmektedir. Cismin ivmesi kaç m/s² dir?",
  "difficulty": "kolay",
  "topic": "Dinamik - Newton'un Hareket Yasaları",
  "options": [
    {"no": 1, "label": "A", "text": "5", "isCorrect": true},
    {"no": 2, "label": "B", "text": "10", "isCorrect": false},
    {"no": 3, "label": "C", "text": "20", "isCorrect": false},
    {"no": 4, "label": "D", "text": "2", "isCorrect": false},
    {"no": 5, "label": "E", "text": "0,5", "isCorrect": false}
  ],
  "explanation": "F = m.a → a = F/m = 10/2 = 5 m/s². B şıkkı F değerini, C F×m hatasını, D m değerini, E m/F hatasını temsil eder."
}`,
	},
	"kimya": {
		SubjectName:       "Kimya",
		SystemPromptExtra: `Bu bir KİMYA sorusudur. Atom yapısı, periyodik tablo, kimyasal bağlar, reaksiyonlar, çözeltiler, organik kimya gibi kimya alt dallarına hakimsin.`,
		DistractorRules: `- Çeldiricilerde periyodik tabloda komşu elementlerin özelliklerini kullan
- Benzer kimyasal formülleri/bileşikleri karıştır
- Denklem denkleştirme hatalarını kullan
- Mol hesabı hatalarının sonuçlarını ekle`,
		FewShotExample: `ÖRNEK SORU:
{
  "questionText": "Aşağıdaki bileşiklerden hangisi iyonik bağ içerir?",
  "difficulty": "orta",
  "topic": "Kimyasal Bağlar",
  "options": [
    {"no": 1, "label": "A", "text": "NaCl", "isCorrect": true},
    {"no": 2, "label": "B", "text": "H₂O", "isCorrect": false},
    {"no": 3, "label": "C", "text": "CO₂", "isCorrect": false},
    {"no": 4, "label": "D", "text": "CH₄", "isCorrect": false},
    {"no": 5, "label": "E", "text": "NH₃", "isCorrect": false}
  ],
  "explanation": "NaCl metal (Na) ve ametal (Cl) arasında oluşan iyonik bağ içerir. Diğerleri ametal-ametal arası kovalent bağlı bileşiklerdir."
}`,
	},
	"tarih": {
		SubjectName:       "Tarih",
		SystemPromptExtra: `Bu bir TARİH sorusudur. Türk tarihi, dünya tarihi, inkılap tarihi, uygarlık tarihi gibi alt dallara hakimsin.`,
		DistractorRules: `- Çeldiricilerde kronolojik olarak yakın dönemlerdeki olayları kullan
- Benzer reformları/antlaşmaları karıştır
- Farklı devletlerin benzer uygulamalarını kullan
- Neden-sonuç ilişkisini ters çeviren seçenekler ekle`,
		FewShotExample: `ÖRNEK SORU:
{
  "questionText": "Osmanlı Devleti'nde ilk anayasal düzenleme aşağıdakilerden hangisidir?",
  "difficulty": "orta",
  "topic": "Osmanlı Modernleşmesi",
  "options": [
    {"no": 1, "label": "A", "text": "Kanun-i Esasi (1876)", "isCorrect": true},
    {"no": 2, "label": "B", "text": "Tanzimat Fermanı (1839)", "isCorrect": false},
    {"no": 3, "label": "C", "text": "Islahat Fermanı (1856)", "isCorrect": false},
    {"no": 4, "label": "D", "text": "Sened-i İttifak (1808)", "isCorrect": false},
    {"no": 5, "label": "E", "text": "II. Meşrutiyet (1908)", "isCorrect": false}
  ],
  "explanation": "Kanun-i Esasi 1876'da ilan edilen ilk Osmanlı anayasasıdır. Diğerleri önemli belgeler olmakla birlikte anayasa niteliği taşımazlar."
}`,
	},
	"edebiyat": {
		SubjectName:       "Türk Dili ve Edebiyatı",
		SystemPromptExtra: `Bu bir EDEBİYAT sorusudur. Türk edebiyatı dönemleri, edebi akımlar, eser-yazar eşleştirmesi, nazım biçimleri, edebi sanatlar gibi alt dallara hakimsin.`,
		DistractorRules: `- Çeldiricilerde aynı dönemin farklı yazarlarını/eserlerini kullan
- Farklı edebi akımların benzer özelliklerini karıştır
- Nazım biçimlerinin benzer özelliklerini kullan
- Edebi sanatları birbirine yakın olanlarla karıştır`,
		FewShotExample: `ÖRNEK SORU:
{
  "questionText": "'Safahat' adlı eser aşağıdaki yazarlardan hangisine aittir?",
  "difficulty": "kolay",
  "topic": "Milli Edebiyat Dönemi",
  "options": [
    {"no": 1, "label": "A", "text": "Mehmet Akif Ersoy", "isCorrect": true},
    {"no": 2, "label": "B", "text": "Yahya Kemal Beyatlı", "isCorrect": false},
    {"no": 3, "label": "C", "text": "Tevfik Fikret", "isCorrect": false},
    {"no": 4, "label": "D", "text": "Ziya Gökalp", "isCorrect": false},
    {"no": 5, "label": "E", "text": "Namık Kemal", "isCorrect": false}
  ],
  "explanation": "Safahat, Mehmet Akif Ersoy'un yedi bölümden oluşan manzum eseridir. Diğer seçenekler aynı veya yakın dönemin önemli şairleridir."
}`,
	},
	"cografya": {
		SubjectName:       "Coğrafya",
		SystemPromptExtra: `Bu bir COĞRAFYA sorusudur. Fiziki coğrafya, beşeri coğrafya, Türkiye coğrafyası, iklim, nüfus, ekonomi gibi alt dallara hakimsin.`,
		DistractorRules: `- Çeldiricilerde komşu bölgelerin/ülkelerin özelliklerini kullan
- Benzer iklim tiplerini karıştır
- Nüfus ve ekonomi verilerinde yakın değerleri kullan
- Yer şekillerinin benzer özelliklerini karıştır`,
		FewShotExample: `ÖRNEK SORU:
{
  "questionText": "Türkiye'de en fazla yağış alan bölge aşağıdakilerden hangisidir?",
  "difficulty": "kolay",
  "topic": "Türkiye İklimi",
  "options": [
    {"no": 1, "label": "A", "text": "Karadeniz Bölgesi", "isCorrect": true},
    {"no": 2, "label": "B", "text": "Marmara Bölgesi", "isCorrect": false},
    {"no": 3, "label": "C", "text": "Akdeniz Bölgesi", "isCorrect": false},
    {"no": 4, "label": "D", "text": "Ege Bölgesi", "isCorrect": false},
    {"no": 5, "label": "E", "text": "Doğu Anadolu Bölgesi", "isCorrect": false}
  ],
  "explanation": "Karadeniz Bölgesi okyanusal iklim etkisiyle Türkiye'nin en çok yağış alan bölgesidir. Her mevsim yağışlıdır."
}`,
	},
	"genel": {
		SubjectName:       "Genel",
		SystemPromptExtra: `Konuyu metnin içeriğinden belirle ve o alana uygun bir soru üret.`,
		DistractorRules: `- Çeldiriciler sorunun konusuyla aynı alandan olmalıdır
- Her çeldirici mantıklı ama yanlış olmalıdır
- Çeldiriciler birbirine yakın kavramlardan seçilmelidir`,
		FewShotExample: "",
	},
}

// subjectKeywords maps keywords to subject identifiers for zero-cost classification
var subjectKeywords = map[string][]string{
	"biyoloji": {"hücre", "mitoz", "mayoz", "dna", "rna", "gen", "protein", "enzim", "organel", "mitokondri",
		"ribozom", "kloroplast", "fotosentez", "solunum", "sinir", "hormon", "endokrin", "sindirim",
		"dolaşım", "boşaltım", "üreme", "kalıtım", "mutasyon", "evrim", "ekosistem", "popülasyon",
		"biyoloji", "organizma", "canlı", "bitki", "hayvan", "bakteri", "virüs", "mantarlar",
		"kromozom", "allel", "fenotip", "genotip", "biyoteknoloji", "ekoloji"},
	"matematik": {"denklem", "fonksiyon", "türev", "integral", "limit", "matris", "determinant",
		"logaritma", "üstel", "polinom", "geometri", "trigonometri", "olasılık", "istatistik",
		"permütasyon", "kombinasyon", "asal", "faktöriyel", "binom", "oran", "orantı",
		"dizi", "seri", "aritmetik", "parabol", "hiperbol", "elips", "çember", "doğru",
		"açı", "üçgen", "kare", "dikdörtgen", "prizma", "küre", "silindir", "koni"},
	"fizik": {"kuvvet", "ivme", "hız", "momentum", "enerji", "iş", "güç", "newton",
		"elektrik", "manyetik", "akım", "gerilim", "direnç", "kapasitör", "indüksiyon",
		"dalga", "frekans", "periyot", "ses", "ışık", "kırılma", "yansıma", "optik",
		"termodinamik", "sıcaklık", "basınç", "hacim", "atom", "çekirdek", "radyoaktif",
		"fizik", "hareket", "sürtünme", "yerçekimi", "potansiyel", "kinetik"},
	"kimya": {"element", "bileşik", "karışım", "atom", "molekül", "iyon", "katyon", "anyon",
		"periyodik", "orbital", "bağ", "kovalent", "iyonik", "metalik", "asit", "baz",
		"tuz", "oksit", "redoks", "elektroliz", "mol", "avogadro", "çözelti", "derişim",
		"organik", "hidrokarbon", "alkan", "alken", "alkin", "alkol", "aldehit", "keton",
		"kimya", "reaksiyon", "denge", "hız", "termodinamik", "kaynama", "erime"},
	"tarih": {"osmanlı", "selçuklu", "cumhuriyet", "atatürk", "inkılap", "savaş", "antlaşma",
		"padişah", "sultan", "sadrazam", "vilayet", "eyalet", "ferman", "kanun",
		"meşrutiyet", "tanzimat", "devrim", "reform", "bağımsızlık", "milli", "mücadele",
		"tarih", "uygarlık", "medeniyet", "imparatorluk", "halifelik", "fetih", "kuruluş",
		"yıkılış", "isyan", "ayaklanma", "kongre", "meclis", "anayasa"},
	"edebiyat": {"şiir", "roman", "hikaye", "tiyatro", "deneme", "makale", "eleştiri",
		"nazım", "nesir", "beyit", "dörtlük", "bent", "aruz", "hece", "kafiye",
		"edebiyat", "edebi", "akım", "divan", "tanzimat", "servet", "fünun", "fecr",
		"milli", "cumhuriyet", "yazar", "şair", "eser", "kahraman", "anlatıcı",
		"mecaz", "benzetme", "istiare", "kinaye", "teşbih", "metonimi"},
	"cografya": {"iklim", "sıcaklık", "yağış", "rüzgar", "basınç", "bölge", "nüfus",
		"göç", "yerleşme", "şehir", "kır", "tarım", "hayvancılık", "sanayi", "ticaret",
		"coğrafya", "dağ", "ova", "plato", "akarsu", "göl", "deniz", "okyanus",
		"kıta", "ülke", "harita", "enlem", "boylam", "meridyen", "paralel",
		"karadeniz", "akdeniz", "ege", "marmara", "anadolu", "trakya", "deprem", "volkan"},
}

func classifySubject(chunk string) string {
	lowerChunk := strings.ToLower(chunk)

	scores := make(map[string]int)
	for subject, keywords := range subjectKeywords {
		for _, keyword := range keywords {
			count := strings.Count(lowerChunk, keyword)
			if count > 0 {
				scores[subject] += count
			}
		}
	}

	bestSubject := "genel"
	bestScore := 0
	for subject, score := range scores {
		if score > bestScore {
			bestScore = score
			bestSubject = subject
		}
	}

	// Minimum eşik: en az 3 keyword match olmalı
	if bestScore < 3 {
		return "genel"
	}

	return bestSubject
}

func buildSubjectSystemPrompt(subject string) string {
	config, exists := subjectPrompts[subject]
	if !exists {
		config = subjectPrompts["genel"]
	}

	basePrompt := fmt.Sprintf(`Sen 20 yıllık deneyime sahip, kıdemli bir ÖSYM soru hazırlayıcısısın.
Uzmanlık alanın: %s

%s

TEMEL GÖREVİN: Verilen metne dayalı, YKS standardında yüksek kaliteli çoktan seçmeli sorular üretmek.

KRİTİK KURALLAR:
1. SORU KÖKÜ: Net, anlaşılır ve tek başına okunduğunda ne sorulduğu anlaşılacak şekilde yaz. 20-40 kelime arasında olmalı.
2. TUTARLILIK: Her şık (A-E), soru köküyle dilbilgisel ve anlamsal olarak uyumlu olmalıdır. Soru kökünü oku, ardından her şıkkı soru köküne ekleyerek oku — cümle doğal ve anlamlı olmalı.
3. KONU BİRLİĞİ: Tüm şıklar aynı konu alanından olmalıdır. Şıklar birbiriyle karşılaştırılabilir nitelikte olmalıdır.
4. ÇELDİRİCİ KALİTESİ:
   - Yanlış şıklar "neredeyse doğru" olmalı — tamamen alakasız veya saçma OLMAMALI.
   - İyi bir çeldirici, konuyu yüzeysel bilen öğrenciyi yanıltabilmeli.
   - Tüm şıklar benzer uzunlukta ve aynı dil yapısında olmalıdır.
   - Bir öğrenci şıklara baktığında hepsinin makul bir cevap olabileceğini düşünmeli.
%s

❌ KÖTÜ ÇELDİRİCİ ÖRNEKLERİ (BUNLARI YAPMA):
- Soruyla tamamen alakasız şıklar (ör: biyoloji sorusunda tarih terimi)
- Açıkça saçma veya mantıksız seçenekler
- Aynı şıkkın farklı kelimelerle tekrarlanması
- Şıklar arasında belirgin uzunluk farkı (doğru cevap çok uzun, diğerleri kısa)

5. ZORLUK: Sorunun zorluk seviyesini belirle (kolay/orta/zor). Orta-zor seviyeyi hedefle.
6. KONU: Sorunun hangi konuyla ilgili olduğunu kısaca belirt.
7. Sadece verilen JSON şemasına uygun çıktı üret.
8. Türkçe yaz.`, config.SubjectName, config.SystemPromptExtra, config.DistractorRules)

	if config.FewShotExample != "" {
		basePrompt += "\n\n" + config.FewShotExample
	}

	return basePrompt
}

var (
	quizQuestionSchema = map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"questionText": map[string]interface{}{
				"type":        "string",
				"description": "Soru kökü metni (20-40 kelime)",
			},
			"difficulty": map[string]interface{}{
				"type":        "string",
				"description": "Sorunun zorluk seviyesi",
				"enum":        []string{"kolay", "orta", "zor"},
			},
			"topic": map[string]interface{}{
				"type":        "string",
				"description": "Sorunun ilgili olduğu konu (ör: Hücre Biyolojisi, Newton Hareket Yasaları)",
			},
			"explanation": map[string]interface{}{
				"type":        "string",
				"description": "Doğru cevabın açıklaması ve diğer şıkların neden yanlış olduğu",
			},
			"options": map[string]interface{}{
				"type": "array",
				"items": map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"no": map[string]interface{}{
							"type":        "integer",
							"description": "Şık numarası (1-5)",
							"minimum":     1,
							"maximum":     5,
						},
						"label": map[string]interface{}{
							"type":        "string",
							"description": "Şık etiketi (A-E)",
							"enum":        []string{"A", "B", "C", "D", "E"},
						},
						"text": map[string]interface{}{
							"type":        "string",
							"description": "Şık metni",
						},
						"isCorrect": map[string]interface{}{
							"type":        "boolean",
							"description": "Bu şıkkın doğru olup olmadığı",
						},
					},
					"required":             []string{"no", "label", "text", "isCorrect"},
					"additionalProperties": false,
				},
				"description": "Tam olarak 5 seçenek listesi",
				"minItems":    5,
				"maxItems":    5,
			},
		},
		"required":             []string{"questionText", "difficulty", "topic", "explanation", "options"},
		"additionalProperties": false,
	}
)

type Config struct {
	ApiKey                 string
	ChatModel              string
	GenerateQuestionModel  string
	ExpandQuestionModel    string
	GenerateChatTitleModel string
	ReasoningModel         string
	VisionModel            string
	EmbeddingModel         string
}

type OpenRouterService struct {
	httpClient *http.Client
	Config
}

func NewOpenRouterService(config Config) *OpenRouterService {
	return &OpenRouterService{
		httpClient: &http.Client{},
		Config:     config,
	}
}

// ----------------------------------------------------------------------
// HELPER METHODS
// ----------------------------------------------------------------------

func (s *OpenRouterService) makeRequest(ctx context.Context, method, endpoint string, body interface{}, respDest interface{}) error {
	var bodyReader io.Reader
	if body != nil {
		jsonData, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("failed to marshal request body: %w", err)
		}
		bodyReader = bytes.NewBuffer(jsonData)
	}

	url := fmt.Sprintf("%s%s", openRouterBaseURL, endpoint)
	req, err := http.NewRequestWithContext(ctx, method, url, bodyReader)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+s.ApiKey)
	// OpenRouter specific headers (optional but good practice)
	req.Header.Set("HTTP-Referer", "https://medaiist.com") // Replace with actual site if strictly needed
	req.Header.Set("X-Title", "MedAIist Backend")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("http request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("api error (status %d): %s", resp.StatusCode, string(bodyBytes))
	}

	if respDest != nil {
		if err := json.NewDecoder(resp.Body).Decode(respDest); err != nil {
			return fmt.Errorf("failed to decode response: %w", err)
		}
	}

	return nil
}

// ----------------------------------------------------------------------
// CHAT
// ----------------------------------------------------------------------

func (s *OpenRouterService) SendChatRequest(ctx context.Context, req *domain.ChatRequest) (*domain.ChatResponse, error) {
	apiMessages := make([]ChatCompletionMessage, 0, len(req.Messages))

	for _, msg := range req.Messages {
		if len(msg.ImageUrl) > 0 {
			parts := []ChatMessagePart{
				{Type: ChatMessagePartTypeText, Text: msg.Content},
			}
			for _, url := range msg.ImageUrl {
				parts = append(parts, ChatMessagePart{
					Type:     ChatMessagePartTypeImageURL,
					ImageURL: &ChatMessageImageURL{URL: url},
				})
			}
			apiMessages = append(apiMessages, ChatCompletionMessage{
				Role:    string(msg.Role),
				Content: parts,
			})
		} else {
			apiMessages = append(apiMessages, ChatCompletionMessage{
				Role:    string(msg.Role),
				Content: msg.Content,
			})
		}
	}

	apiReq := ChatCompletionRequest{
		Model:    s.ChatModel,
		Messages: apiMessages,
		Stream:   req.Stream,
	}

	var apiResp ChatCompletionResponse
	if err := s.makeRequest(ctx, "POST", "/chat/completions", apiReq, &apiResp); err != nil {
		return nil, err
	}

	if len(apiResp.Choices) == 0 {
		return nil, fmt.Errorf("empty response")
	}

	content := ""
	if str, ok := apiResp.Choices[0].Message.Content.(string); ok {
		content = str
	} else if parts, ok := apiResp.Choices[0].Message.Content.([]interface{}); ok {
		for _, p := range parts {
			if m, ok := p.(map[string]interface{}); ok {
				if t, ok := m["text"].(string); ok {
					content += t
				}
			}
		}
	}

	return &domain.ChatResponse{
		Text: content,
	}, nil
}

func (s *OpenRouterService) GenerateChatTitle(ctx context.Context, req *domain.ChatRequest) (*domain.ChatResponse, error) {
	apiMessages := make([]ChatCompletionMessage, 0, len(req.Messages))
	for _, msg := range req.Messages {
		apiMessages = append(apiMessages, ChatCompletionMessage{
			Role:    string(msg.Role),
			Content: msg.Content,
		})
	}

	apiReq := ChatCompletionRequest{
		Model:    s.GenerateChatTitleModel,
		Messages: apiMessages,
	}

	var apiResp ChatCompletionResponse
	if err := s.makeRequest(ctx, "POST", "/chat/completions", apiReq, &apiResp); err != nil {
		return nil, fmt.Errorf("failed to generate title: %w", err)
	}
	if len(apiResp.Choices) == 0 {
		return nil, fmt.Errorf("empty response")
	}

	content, _ := apiResp.Choices[0].Message.Content.(string)
	return &domain.ChatResponse{
		Text: content,
	}, nil
}

// ----------------------------------------------------------------------
// CLASSIFICATION & EXPANSION
// ----------------------------------------------------------------------

func (s *OpenRouterService) ExpandQuery(ctx context.Context, content string) ([]string, error) {
	apiReq := ChatCompletionRequest{
		Model: s.ExpandQuestionModel,
		Messages: []ChatCompletionMessage{
			{Role: "system", Content: queryExpansionSystemPrompt},
			{Role: "user", Content: content},
		},
		ResponseFormat: &ResponseFormat{Type: "json_object"},
	}

	var apiResp ChatCompletionResponse
	if err := s.makeRequest(ctx, "POST", "/chat/completions", apiReq, &apiResp); err != nil {
		return nil, fmt.Errorf("expand query failed: %w", err)
	}
	if len(apiResp.Choices) == 0 {
		return nil, fmt.Errorf("empty response")
	}

	respContent, _ := apiResp.Choices[0].Message.Content.(string)

	var parsed domain.QueryExpansionResponse
	if err := json.Unmarshal([]byte(respContent), &parsed); err != nil {
		return []string{content}, nil
	}

	return parsed.Queries, nil
}

// ----------------------------------------------------------------------
// QUIZ
// ----------------------------------------------------------------------

func (s *OpenRouterService) GenerateQuizQuestion(ctx context.Context, chunk string) (*domain.QuizQuestion, error) {
	// Auto-detect subject from chunk content (zero-cost, keyword-based)
	subject := classifySubject(chunk)
	systemPrompt := buildSubjectSystemPrompt(subject)

	prompt := fmt.Sprintf(`Aşağıdaki metin parçasını dikkatlice oku ve bu metne dayalı bir çoktan seçmeli soru üret.

--- METİN BAŞLANGIÇ ---
%s
--- METİN BİTİŞ ---

SORU ÜRETME ADIMLARI (Sırasıyla uygula, her adımı düşün):

1. METİN ANALİZİ: Metindeki en önemli kavramı, ilişkiyi veya bilgiyi belirle. Konuyu "topic" alanına yaz.
2. ZORLUK BELİRLE: Sorunun zorluk seviyesini belirle ve "difficulty" alanına yaz.
3. SORU KÖKÜ YAZIMI: Bu kavramı ölçen, net ve anlaşılır bir soru kökü yaz (20-40 kelime).
   - Soru kökü tek başına okunduğunda ne sorulduğu anlaşılmalı.
   - ÖSYM/YKS tarzında olmalı.
4. DOĞRU ŞIK: Metne dayalı olarak doğru cevabı yaz.
5. ÇELDİRİCİ ŞIKLAR: Kalan 4 şıkkı oluştururken:
   a) Her çeldirici, soru kökünü doğal şekilde tamamlamalı veya cevaplamalıdır.
   b) Çeldiriciler, sorunun konusuyla AYNI konu alanından olmalıdır.
   c) Çeldiriciler mantıklı ama yanlış olmalıdır — rastgele veya alakasız bilgi YAZMA.
   d) Tüm şıklar benzer uzunlukta ve dil yapısında olmalıdır.
   e) Bir öğrenci şıklara baktığında hepsinin makul bir cevap olabileceğini düşünmeli.
6. KENDİ KENDİNİ KONTROL ET: Soru kökünü oku, sonra her şıkkı soru köküne ekleyerek oku. Cümle doğal mı? Şıklar aynı kategoriden mi? Alakasız şık var mı?
7. AÇIKLAMA: Doğru cevabın neden doğru olduğunu VE diğer şıkların neden yanlış olduğunu kısaca açıkla.

FORMAT KURALLARI:
- Tam 5 şık (A-E), sadece 1 tanesi doğru.
- Türkçe yaz.
- SADECE JSON çıktısı ver, başka metin yazma.`, chunk)

	schemaJSON, err := json.Marshal(quizQuestionSchema)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal schema: %w", err)
	}

	apiReq := ChatCompletionRequest{
		Model: s.GenerateQuestionModel,
		Messages: []ChatCompletionMessage{
			{
				Role:    "system",
				Content: systemPrompt,
			},
			{
				Role:    "user",
				Content: prompt,
			},
		},
		ResponseFormat: &ResponseFormat{
			Type: "json_schema",
			JSONSchema: &ResponseFormatSchema{
				Name:   "quiz_question",
				Schema: json.RawMessage(schemaJSON),
				Strict: true,
			},
		},
		Temperature: 0.4,
	}

	question, err := s.executeQuizRequest(ctx, apiReq)
	if err != nil {
		// Retry once on failure
		question, err = s.executeQuizRequest(ctx, apiReq)
		if err != nil {
			return nil, fmt.Errorf("quiz generation failed after retry: %w", err)
		}
	}

	return question, nil
}

// executeQuizRequest sends the API request and validates the response
func (s *OpenRouterService) executeQuizRequest(ctx context.Context, apiReq ChatCompletionRequest) (*domain.QuizQuestion, error) {
	var apiResp ChatCompletionResponse
	if err := s.makeRequest(ctx, "POST", "/chat/completions", apiReq, &apiResp); err != nil {
		return nil, fmt.Errorf("quiz generation failed: %w", err)
	}
	if len(apiResp.Choices) == 0 {
		return nil, fmt.Errorf("empty quiz response")
	}

	respContent, _ := apiResp.Choices[0].Message.Content.(string)
	content := strings.TrimSpace(respContent)

	var question domain.QuizQuestion
	if err := json.Unmarshal([]byte(content), &question); err != nil {
		return nil, fmt.Errorf("invalid quiz JSON: %w (content: %s)", err, content)
	}

	// Structural validation
	if question.QuestionText == "" {
		return nil, fmt.Errorf("question text is empty")
	}
	if len(question.Options) != 5 {
		return nil, fmt.Errorf("options count incorrect: %d", len(question.Options))
	}

	// Validate exactly 1 correct option
	correctCount := 0
	for _, o := range question.Options {
		if o.IsCorrect {
			question.CorrectOptionLabel = o.Label
			correctCount++
		}
	}
	if correctCount != 1 {
		return nil, fmt.Errorf("expected exactly 1 correct option, got %d", correctCount)
	}

	// Quality validation: check option length consistency
	var totalLen int
	for _, o := range question.Options {
		totalLen += len(o.Text)
	}
	avgLen := totalLen / 5
	for _, o := range question.Options {
		optLen := len(o.Text)
		// If any option is 5x longer or shorter than average, flag it
		if avgLen > 0 && (optLen > avgLen*5 || optLen < avgLen/5) {
			return nil, fmt.Errorf("option length inconsistency detected (option %s: %d chars, avg: %d chars)", o.Label, optLen, avgLen)
		}
	}

	// Quality validation: check minimum question length
	if len(question.QuestionText) < 20 {
		return nil, fmt.Errorf("question text too short: %d chars", len(question.QuestionText))
	}

	return &question, nil
}

// ----------------------------------------------------------------------
// VISION & REASONING
// ----------------------------------------------------------------------

func (s *OpenRouterService) ExtractTextFromImage(ctx context.Context, imageBase64 string) (string, error) {
	if !strings.HasPrefix(imageBase64, "data:image") {
		imageBase64 = "data:image/jpeg;base64," + imageBase64
	}

	parts := []ChatMessagePart{
		{Type: ChatMessagePartTypeText, Text: "Sen bir OCR motorusun. Resimdeki sorudaki metinleri ve çizimleri çıkar. Sakın soruyu çözme. Sadece text çıktısı ver."},
		{Type: ChatMessagePartTypeImageURL, ImageURL: &ChatMessageImageURL{URL: imageBase64}},
	}

	apiReq := ChatCompletionRequest{
		Model: s.VisionModel,
		Messages: []ChatCompletionMessage{
			{Role: "user", Content: parts},
		},
	}

	var apiResp ChatCompletionResponse
	if err := s.makeRequest(ctx, "POST", "/chat/completions", apiReq, &apiResp); err != nil {
		return "", fmt.Errorf("ocr/vision request failed: %w", err)
	}
	if len(apiResp.Choices) == 0 {
		return "", fmt.Errorf("empty ocr response")
	}

	content, _ := apiResp.Choices[0].Message.Content.(string)
	return content, nil
}

func (s *OpenRouterService) ExtractFromImage(ctx context.Context, imageBase64 string) (*domain.ExtractionResult, error) {
	text, err := s.ExtractTextFromImage(ctx, imageBase64)
	if err != nil {
		return nil, err
	}
	return &domain.ExtractionResult{
		Text: text,
	}, nil
}

type ytTranscript struct {
	Events []struct {
		Segs []struct {
			Utf8 string `json:"utf8"`
		} `json:"segs"`
	} `json:"events"`
}

type headerTransport struct {
	rt http.RoundTripper
}

func (t *headerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	return t.rt.RoundTrip(req)
}

func (s *OpenRouterService) ExtractFromYouTube(ctx context.Context, url string) (*domain.ExtractionResult, error) {
	client := youtube.Client{
		HTTPClient: &http.Client{
			Transport: &headerTransport{rt: http.DefaultTransport},
		},
	}
	video, err := client.GetVideo(url)
	if err != nil {
		return nil, fmt.Errorf("failed to get video info: %w", err)
	}

	title := video.Title
	if title == "" {
		title = "YouTube Video"
	}

	thumbnailUrl := ""
	if len(video.Thumbnails) > 0 {
		thumbnailUrl = video.Thumbnails[0].URL
	} else {
		// Fallback extraction
		if strings.Contains(url, "v=") {
			parts := strings.Split(url, "v=")
			if len(parts) > 1 {
				vidID := strings.Split(parts[1], "&")[0]
				thumbnailUrl = fmt.Sprintf("https://img.youtube.com/vi/%s/hqdefault.jpg", vidID)
			}
		} else if strings.Contains(url, "youtu.be/") {
			parts := strings.Split(url, "youtu.be/")
			if len(parts) > 1 {
				vidID := strings.Split(parts[1], "?")[0]
				thumbnailUrl = fmt.Sprintf("https://img.youtube.com/vi/%s/hqdefault.jpg", vidID)
			}
		}
	}

	var rawTranscript string
	if len(video.CaptionTracks) > 0 {
		trackUrl := video.CaptionTracks[0].BaseURL
		trackUrl = strings.Replace(trackUrl, "fmt=srv3", "fmt=json3", 1)

		req, err := http.NewRequestWithContext(ctx, "GET", trackUrl, nil)
		if err == nil {
			resp, err := http.DefaultClient.Do(req)
			if err == nil {
				defer resp.Body.Close()
				body, err := io.ReadAll(resp.Body)
				if err == nil {
					var t ytTranscript
					if err := json.Unmarshal(body, &t); err == nil {
						for _, e := range t.Events {
							for _, seg := range e.Segs {
								if seg.Utf8 != "\n" {
									rawTranscript += seg.Utf8 + " "
								}
							}
						}
					}
				}
			}
		}
	}

	var finalContent string

	if rawTranscript == "" {
		// If no transcript could be found, ask the LLM to write a general note about the title.
		prompt := fmt.Sprintf("Aşağıdaki YouTube videosunun alt yazısı bulunamadı. Lütfen sadece bu videonun başlığına bakarak video hakkında çok genel bilgiler içeren kısa bir not oluştur.\n\nVideo Başlığı: %s", title)

		apiReq := ChatCompletionRequest{
			Model: s.ChatModel,
			Messages: []ChatCompletionMessage{
				{Role: "user", Content: prompt},
			},
		}
		var apiResp ChatCompletionResponse
		if err := s.makeRequest(ctx, "POST", "/chat/completions", apiReq, &apiResp); err == nil && len(apiResp.Choices) > 0 {
			finalContent, _ = apiResp.Choices[0].Message.Content.(string)
		} else {
			return nil, fmt.Errorf("youtube extraction request failed: %w", err)
		}
	} else {
		// Summarize raw transcript
		prompt := fmt.Sprintf("Aşağıda bir YouTube videosunun ham transkripti verilmiştir. Lütfen bu videonun tüm içeriğini, konuşmaları ve önemli bilgileri detaylı, okunabilir bir not halinde çıkar. Sadece notları dön, başka bir şey yazma.\n\nTranskript:\n%s", rawTranscript)

		apiReq := ChatCompletionRequest{
			Model: s.ChatModel,
			Messages: []ChatCompletionMessage{
				{Role: "user", Content: prompt},
			},
		}
		var apiResp ChatCompletionResponse
		if err := s.makeRequest(ctx, "POST", "/chat/completions", apiReq, &apiResp); err == nil && len(apiResp.Choices) > 0 {
			finalContent, _ = apiResp.Choices[0].Message.Content.(string)
		} else {
			return nil, fmt.Errorf("youtube summarization request failed: %w", err)
		}
	}

	return &domain.ExtractionResult{
		Text:         strings.TrimSpace(finalContent),
		Title:        title,
		ThumbnailUrl: thumbnailUrl,
	}, nil
}

func (s *OpenRouterService) SolveQuestionWithReasoning(ctx context.Context, questionText string) (string, error) {
	apiReq := ChatCompletionRequest{
		Model: s.ReasoningModel,
		Messages: []ChatCompletionMessage{
			{
				Role: "system",
				Content: `Soru çözücüsün. Giriş cümlesi olmadan direkt çözüme başla.

FORMAT (Markdown kullan):
### Konu: [Konuyu kısa belirt]

###  Çözüm
Adımları numaralı liste halinde yaz. Her adımda:
- Önemli terimleri **kalın** yap
- Formülleri LaTeX formatında yaz: $formül$
- Gerekirse kısa açıklama ekle

### Cevap
Sonucu net ve belirgin şekilde yaz.

KURALLAR:
- Kendini tanıtma, selamlama, "Merhaba", "Hadi bakalım" gibi dolgu cümleler YAZMA
- Soruyu tekrar alıntılama
- Her adım öz ve anlaşılır olsun, ama gerekli açıklamayı atla demek değil
- Türkçe yanıtla`,
			},
			{
				Role:    "user",
				Content: questionText,
			},
		},
		Temperature: 0.4,
	}

	var apiResp ChatCompletionResponse
	if err := s.makeRequest(ctx, "POST", "/chat/completions", apiReq, &apiResp); err != nil {
		return "", fmt.Errorf("reasoning/solving failed: %w", err)
	}
	if len(apiResp.Choices) == 0 {
		return "", fmt.Errorf("empty reasoning response")
	}

	content, _ := apiResp.Choices[0].Message.Content.(string)
	return content, nil
}

// ----------------------------------------------------------------------
// EMBEDDING
// ----------------------------------------------------------------------

func (s *OpenRouterService) GenerateEmbedding(ctx context.Context, req *domain.SingleEmbeddingRequest) (*domain.SingleEmbeddingResponse, error) {
	apiReq := EmbeddingRequest{
		Input: req.Text,
		Model: s.EmbeddingModel,
	}

	var apiResp EmbeddingResponse
	if err := s.makeRequest(ctx, "POST", "/embeddings", apiReq, &apiResp); err != nil {
		return nil, fmt.Errorf("failed to create embedding: %w", err)
	}

	if len(apiResp.Data) == 0 {
		return nil, fmt.Errorf("no embedding data received")
	}

	src := apiResp.Data[0].Embedding
	vector := make([]float32, len(src))
	for i, v := range src {
		vector[i] = float32(v)
	}

	return &domain.SingleEmbeddingResponse{
		Vector: vector,
	}, nil
}

func (s *OpenRouterService) GenerateEmbeddingBatch(ctx context.Context, req *domain.EmbeddingBatchRequest) (*domain.EmbeddingResponse, error) {
	// []Text -> []string
	inputs := make([]string, len(req.Texts))
	for i, t := range req.Texts {
		inputs[i] = t.Text
	}

	apiReq := EmbeddingRequest{
		Input: inputs,
		Model: s.EmbeddingModel,
	}

	var apiResp EmbeddingResponse
	if err := s.makeRequest(ctx, "POST", "/embeddings", apiReq, &apiResp); err != nil {
		return nil, fmt.Errorf("failed to create embeddings: %w", err)
	}

	results := make([]domain.Embedding, len(apiResp.Data))
	for i, data := range apiResp.Data {
		vector := make([]float32, len(data.Embedding))
		for j, v := range data.Embedding {
			vector[j] = float32(v)
		}

		results[i] = domain.Embedding{
			Id:          req.Texts[i].Id,
			Text:        req.Texts[i].Text,
			Vector:      vector,
			PageNumbers: []int{},
		}
	}

	if len(results) == 0 {
		return nil, fmt.Errorf("empty embedding results")
	}

	return &domain.EmbeddingResponse{
		Results:   results,
		Dimension: len(results[0].Vector),
		Count:     len(results),
	}, nil
}
