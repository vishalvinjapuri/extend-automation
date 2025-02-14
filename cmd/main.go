package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"local/extend"
	"local/extend/cognito"

	"github.com/bwmarrin/discordgo"
	"github.com/joho/godotenv"
)

var userStates = make(map[string]string)
var cardDetails = make(map[string]map[string]string)

func main() {
	err := godotenv.Load()
	if err != nil {
		log.Fatalf("Error loading .env file")
	}

	discordToken := os.Getenv("DISCORD_BOT_TOKEN")
	if discordToken == "" {
		log.Fatalf("No Discord bot token provided")
	}

	dg, err := discordgo.New("Bot " + discordToken)
	if err != nil {
		log.Fatalf("Error creating Discord session: %v", err)
	}

	dg.AddHandler(messageCreate)

	err = dg.Open()
	if err != nil {
		log.Fatalf("Error opening Discord session: %v", err)
	}

	log.Println("Bot is now running. Press CTRL+C to exit.")
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM, os.Interrupt, os.Kill)
	<-stop

	dg.Close()
}

func messageCreate(s *discordgo.Session, m *discordgo.MessageCreate) {
	if m.Author.ID == s.State.User.ID {
		return
	}

	userID := m.Author.ID

	switch userStates[userID] {
	case "":
		if strings.HasPrefix(m.Content, "!create") {
			parts := strings.Fields(m.Content)
			if len(parts) != 3 {
				s.ChannelMessageSend(m.ChannelID, "Usage: !create <DisplayName> <BalanceInDollars>")
				return
			}

			displayName := parts[1]
			balanceDollars := parts[2]

			if err := handleCardCreation(s, m, userID, displayName, balanceDollars); err != nil {
				s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("Error: %v", err))
			}
		} else if strings.HasPrefix(m.Content, "!close") {
			parts := strings.Fields(m.Content)
			if len(parts) != 2 {
				s.ChannelMessageSend(m.ChannelID, "Usage: !close <VC_ID>")
				return
			}

			vcID := parts[1]
			if err := handleCardClosure(s, m, vcID); err != nil {
				s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("Error: %v", err))
			}
		}
	case "awaitingDisplayName":
		cardDetails[userID]["DisplayName"] = m.Content
		userStates[userID] = "awaitingBalance"
		s.ChannelMessageSend(m.ChannelID, "Please reply with the balance in dollars for the card.")
	case "awaitingBalance":
		displayName := cardDetails[userID]["DisplayName"]
		balanceDollars := m.Content

		if err := handleCardCreation(s, m, userID, displayName, balanceDollars); err != nil {
			s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("Error: %v", err))
		}
		delete(cardDetails, userID)
	}
}

func handleCardCreation(s *discordgo.Session, m *discordgo.MessageCreate, userID, displayName, balanceDollars string) error {
	balanceCents, err := convertDollarsToCents(balanceDollars)
	if err != nil {
		return fmt.Errorf("invalid balance amount: %v", err)
	}

	card, err := createVirtualCard(displayName, strconv.Itoa(balanceCents))
	if err != nil {
		return fmt.Errorf("failed to create virtual card: %v", err)
	}

	log.Printf("Card API Response: %+v\n", card)

	if card == nil || card.Vcn == nil || card.SecurityCode == nil {
		return fmt.Errorf("card details are incomplete")
	}

	vcn := *card.Vcn
	securityCode := *card.SecurityCode
	expiryDate := card.Expires.Format("01/2006")
	cardLimit := card.LimitCents
	cardVCID := card.ID

	log.Printf("Virtual Card Details - VCN: %s, Security Code: %s, Expiry Date: %s\n", vcn, securityCode, expiryDate)

	s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("**Virtual Card Created**\nName: %s", card.DisplayName))
	s.ChannelMessageSend(m.ChannelID, "Card Number:")
	s.ChannelMessageSend(m.ChannelID, vcn)
	s.ChannelMessageSend(m.ChannelID, "CVV:")
	s.ChannelMessageSend(m.ChannelID, securityCode)
	s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("Expiry Date: %s", expiryDate))
	s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("Card Limit: %s", formatLimit(cardLimit)))
	s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("VC ID: %s", cardVCID))

	return nil
}

func convertDollarsToCents(dollars string) (int, error) {
	amount, err := strconv.ParseFloat(dollars, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid dollar amount: %v", err)
	}
	return int(amount * 100), nil
}

func createVirtualCard(displayName, balanceCents string) (*extend.VirtualCard, error) {
	username := os.Getenv("COGNITO_USERNAME")
	password := os.Getenv("COGNITO_PASSWORD")
	deviceGroupKey := os.Getenv("COGNITO_DEVICE_GROUP_KEY")
	deviceKey := os.Getenv("COGNITO_DEVICE_KEY")
	devicePassword := os.Getenv("COGNITO_DEVICE_PASSWORD")

	auth := cognito.NewCognito(cognito.AuthParams{
		Username:       username,
		Password:       password,
		DeviceGroupKey: deviceGroupKey,
		DeviceKey:      deviceKey,
		DevicePassword: devicePassword,
	})

	client := extend.New(auth)

	balance, err := strconv.Atoi(balanceCents)
	if err != nil {
		return nil, fmt.Errorf("invalid balance: %v", err)
	}

	initialCard, err := client.CreateVirtualCard(context.Background(), extend.CreateVirtualCardOptions{
		CreditCardID: os.Getenv("CREDIT_CARD_ID"),
		DisplayName:  displayName,
		BalanceCents: balance,
		Currency:     extend.CurrencyUSD,
		ValidTo:      time.Now().AddDate(0, 1, 0),
		Recipient:    os.Getenv("RECIPIENT"),
		Notes:        "",
	})
	if err != nil {
		return nil, err
	}
	card, err := client.GetVirtualCard(context.Background(), initialCard.ID)
	if err != nil {
		return nil, fmt.Errorf("failed to get virtual card: %v", err)
	}

	return card, nil
}

func formatLimit(cents int) string {
	return fmt.Sprintf("$%.2f", float64(cents)/100)
}

func handleCardClosure(s *discordgo.Session, m *discordgo.MessageCreate, vcID string) error {

	username := os.Getenv("COGNITO_USERNAME")
	password := os.Getenv("COGNITO_PASSWORD")
	deviceGroupKey := os.Getenv("COGNITO_DEVICE_GROUP_KEY")
	deviceKey := os.Getenv("COGNITO_DEVICE_KEY")
	devicePassword := os.Getenv("COGNITO_DEVICE_PASSWORD")

	auth := cognito.NewCognito(cognito.AuthParams{
		Username:       username,
		Password:       password,
		DeviceGroupKey: deviceGroupKey,
		DeviceKey:      deviceKey,
		DevicePassword: devicePassword,
	})

	client := extend.New(auth)

	_, err := client.CloseVirtualCard(context.Background(), vcID)
	if err != nil {
		return fmt.Errorf("failed to close virtual card: %v", err)
	}

	s.ChannelMessageSend(m.ChannelID, fmt.Sprintf("Virtual Card with ID %s has been successfully closed.", vcID))
	return nil
}
