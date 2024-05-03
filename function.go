package helloworld

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"io"
	"log"
	"os"

	firebase "firebase.google.com/go"
	"firebase.google.com/go/db"
	"github.com/GoogleCloudPlatform/functions-framework-go/functions"
	"google.golang.org/api/option"

	"github.com/google/generative-ai-go/genai"
	"github.com/line/line-bot-sdk-go/v8/linebot"
	"github.com/line/line-bot-sdk-go/v8/linebot/messaging_api"
	"github.com/line/line-bot-sdk-go/v8/linebot/webhook"
)

// Define the context
var fireDB FireDB

// LINE BOt sdk
var bot *messaging_api.MessagingApiAPI
var blob *messaging_api.MessagingApiBlobAPI
var channelToken string

// Gemni API key
var geminiKey string

// define firebase db
type FireDB struct {
	*db.Client
}

// Define your custom struct for Gemini ChatMemory
type GeminiChat struct {
	Parts []string `json:"parts"`
	Role  string   `json:"role"`
}

func init() {
	var err error
	// Init firebase related variables
	ctx := context.Background()
	opt := option.WithCredentialsJSON([]byte(os.Getenv("GOOGLE_APPLICATION_CREDENTIALS")))
	config := &firebase.Config{DatabaseURL: os.Getenv("FIREBASE_URL")}
	app, err := firebase.NewApp(ctx, config, opt)
	if err != nil {
		log.Fatalf("error initializing app: %v", err)
	}
	client, err := app.Database(ctx)
	if err != nil {
		log.Fatalf("error initializing database: %v", err)
	}
	fireDB.Client = client

	// Init LINE Bot related variables
	geminiKey = os.Getenv("GOOGLE_GEMINI_API_KEY")
	channelToken = os.Getenv("ChannelAccessToken")
	bot, err = messaging_api.NewMessagingApiAPI(channelToken)
	if err != nil {
		log.Fatal(err)
	}

	blob, err = messaging_api.NewMessagingApiBlobAPI(channelToken)
	if err != nil {
		log.Fatal(err)
	}

	functions.HTTP("HelloHTTP", HelloHTTP)
}

func HelloHTTP(w http.ResponseWriter, r *http.Request) {
	ctx := context.Background()

	cb, err := webhook.ParseRequest(os.Getenv("ChannelSecret"), r)
	if err != nil {
		if err == linebot.ErrInvalidSignature {
			w.WriteHeader(400)
		} else {
			w.WriteHeader(500)
		}
		return
	}

	for _, event := range cb.Events {
		log.Printf("Got event %v", event)
		switch e := event.(type) {
		case webhook.MessageEvent:
			switch message := e.Message.(type) {

			// Handle only text messages
			case webhook.TextMessageContent:
				req := message.Text

				// Get the conversation from the firebase
				var Memory []*genai.Content
				var DbData []GeminiChat
				err = fireDB.NewRef("BwAI").Get(ctx, &DbData)
				if err != nil {
					fmt.Println("load memory failed, ", err)
				}

				// convert InMemory to Memory
				for _, c := range DbData {
					parts := make([]genai.Part, len(c.Parts))
					for i, part := range c.Parts {
						parts[i] = genai.Text(part)
					}
					dst := &genai.Content{
						Parts: parts,
						Role:  c.Role,
					}

					Memory = append(Memory, dst)
				}

				ctx := context.Background()
				client, err := genai.NewClient(ctx, option.WithAPIKey(geminiKey))
				if err != nil {
					log.Fatal(err)
				}
				defer client.Close()

				// Pass the text content to the gemini-pro model for text generation
				model := client.GenerativeModel("gemini-pro")

				//workaround for the issue unknown field "usageMetadata" in googleapi.

				//get json from DbData
				var jsonStr []byte
				jsonStr, err = json.Marshal(DbData)
				if err != nil {
					fmt.Println("json marshal failed, ", err)
				}

				totalString := fmt.Sprintf("Memory:(%s), %s", string(jsonStr), req)
				res, err := model.GenerateContent(ctx, genai.Text(totalString))
				if err != nil {
					log.Fatal(err)
				}

				// cs := model.StartChat()
				// cs.History = Memory
				// res, err := cs.SendMessage(ctx, genai.Text(req))
				// if err != nil {
				// 	log.Fatal(err)
				// }
				var ret string
				for _, cand := range res.Candidates {
					for _, part := range cand.Content.Parts {
						ret = ret + fmt.Sprintf("%v", part)
						log.Println(part)
					}
				}

				// Save the conversation to the memory
				Memory = append(Memory, &genai.Content{
					Parts: []genai.Part{
						genai.Text(req),
					},
					Role: "user",
				})

				// Save the response to the memory
				Memory = append(Memory, &genai.Content{
					Parts: []genai.Part{
						genai.Text(ret),
					},
					Role: "model",
				})

				// Save the conversation to the firebase
				err = fireDB.NewRef("BwAI").Set(ctx, Memory)
				if err != nil {
					fmt.Println(err)
					return
				}

				// Reply message
				if _, err := bot.ReplyMessage(
					&messaging_api.ReplyMessageRequest{
						ReplyToken: e.ReplyToken,
						Messages: []messaging_api.MessageInterface{
							&messaging_api.TextMessage{
								Text: ret,
							},
						},
					},
				); err != nil {
					log.Print(err)
					return
				}

			// Handle only image messages
			case webhook.ImageMessageContent:
				log.Println("Got img msg ID:", message.Id)

				// Get image content through message.Id
				content, err := blob.GetMessageContent(message.Id)
				if err != nil {
					log.Println("Got GetMessageContent err:", err)
				}
				// Read image content
				defer content.Body.Close()
				data, err := io.ReadAll(content.Body)
				if err != nil {
					log.Fatal(err)
				}
				ctx := context.Background()
				client, err := genai.NewClient(ctx, option.WithAPIKey(geminiKey))
				if err != nil {
					log.Fatal(err)
				}
				defer client.Close()

				// Pass the image content to the gemini-pro-vision model for image description
				model := client.GenerativeModel("gemini-pro-vision")
				prompt := []genai.Part{
					genai.ImageData("png", data),
					genai.Text("Describe this image with scientific detail, reply in zh-TW:"),
				}
				resp, err := model.GenerateContent(ctx, prompt...)
				if err != nil {
					log.Fatal(err)
				}

				// Get the returned content
				var ret string
				for _, cand := range resp.Candidates {
					for _, part := range cand.Content.Parts {
						ret = ret + fmt.Sprintf("%v", part)
						log.Println(part)
					}
				}

				// Reply message
				if _, err := bot.ReplyMessage(
					&messaging_api.ReplyMessageRequest{
						ReplyToken: e.ReplyToken,
						Messages: []messaging_api.MessageInterface{
							&messaging_api.TextMessage{
								Text: ret,
							},
						},
					},
				); err != nil {
					log.Print(err)
					return
				}

			// Handle only video message
			case webhook.VideoMessageContent:
				log.Println("Got video msg ID:", message.Id)

			default:
				log.Printf("Unknown message: %v", message)
			}
		case webhook.FollowEvent:
			log.Printf("message: Got followed event")
		case webhook.PostbackEvent:
			data := e.Postback.Data
			log.Printf("Unknown message: Got postback: " + data)
		case webhook.BeaconEvent:
			log.Printf("Got beacon: " + e.Beacon.Hwid)
		}
	}
}
