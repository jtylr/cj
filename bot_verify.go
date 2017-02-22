package main

import (
	"crypto/rand"
	"encoding/base64"
	"strings"

	"regexp"
	"strconv"

	"fmt"

	"github.com/bwmarrin/discordgo"
	gocache "github.com/patrickmn/go-cache"
)

/*
Verification process:
- > User issues command "verify" to the bot via direct message
- < Bot informs the user of the verification process and queries the user for
    their user profile page URL
- > User provides user profile page URL, with or without the protocol qualifier
    or just the user ID
- < Bot generates a unique token and provides it to the user, asks user to post
    it on their user Bio or Visitor Messages section and reply to the bot with
    either "done" or "cancel", any other string repeates the previous message
- > User posts the unique onto their Bio or Visitor Messages section and writes
    "done" back to the bot
- < Bot checks the Bio and Visitor Messages sections of the user profile page
    and verifies that the unique code provided to the user is present on the
    page then stores the information to the database and purges the Verification
    object from the local cache
*/

// VerificationState represents the state a user's verification process is in.
type VerificationState int32

const (
	// VerificationStateNone represents the state a user is in before or after
	// the verification process. In other words, if a Verification is in this
	// state it means an error has occurred and the Verification should be
	// purged from the cache.
	VerificationStateNone VerificationState = iota

	// VerificationStateAwaitProfileURL is when the bot is waiting for the user
	// to provide their user profile page URL.
	VerificationStateAwaitProfileURL VerificationState = iota

	// VerificationStateAwaitConfirmation is when the bot is waiting for the
	// user to reply with either "done" or "cancel"
	VerificationStateAwaitConfirmation VerificationState = iota
)

// Verification holds all the state for a verification process
type Verification struct {
	channelID   string
	discordUser discordgo.User
	forumUser   string
	code        string
	verifyState VerificationState
}

// SetVerificationState updates the state of a Verification and ensures it's
// cache entry is updated.
func (app App) SetVerificationState(v *Verification, state VerificationState) {
	v.verifyState = state
	app.cache.Set(v.discordUser.ID, *v, gocache.DefaultExpiration)
}

// UserStartsVerification is called when the user sends the string "verify" to
// the bot.
func (app App) UserStartsVerification(message discordgo.Message) error {
	var verification Verification
	var err error

	result, found := app.cache.Get(message.Author.ID)
	// At this point, it should not be found because this is the point where
	// a user should be starting their verification and thus there should be
	// no trace of their Verification in the cache.
	if found {
		verification = result.(Verification)
		if err = app.WarnUserVerificationState(message.ChannelID, verification); err != nil {
			return err
		}
		return nil
	}

	_, err = app.discordClient.ChannelMessageSend(
		message.ChannelID,
		`Hi! This process will verify you are the owner of a SA:MP forum account. Please provide your user profile URL or ID.

        Examples:

        http://forum.sa-mp.com/member.php?u=50199
        forum.sa-mp.com/member.php?u=50199
        50199
        
        Each stage of the verification process will time-out after 5 minutes, if you take longer than that to respond you will need to start again.`)
	if err != nil {
		return err
	}

	v := Verification{discordUser: *message.Author, channelID: message.ChannelID}
	app.SetVerificationState(&v, VerificationStateAwaitProfileURL)

	return nil
}

// UserProvidesProfileURL is called when the user responds with a profile URL or
// profile ID.
func (app App) UserProvidesProfileURL(message discordgo.Message) error {
	var verification Verification
	var err error

	result, found := app.cache.Get(message.Author.ID)
	if !found {
		err = app.WarnUserNoVerification(message.ChannelID)
		return err
	}

	verification = result.(Verification)

	if verification.verifyState != VerificationStateAwaitProfileURL {
		err = app.WarnUserVerificationState(message.ChannelID, verification)
		return err
	}

	matched, err := regexp.MatchString(`^(http:\/\/)?forum\.sa-mp\.com\/member\.php\?u=[0-9]*$`, message.Content)
	if err != nil {
		err = app.WarnUserBadInput(message.ChannelID, verification)
		return err
	}

	var profileURL string

	// If it didn't match, check if it's just a user ID
	if matched {
		if strings.HasPrefix(message.Content, "http://") {
			profileURL = message.Content
		} else {
			profileURL = "http://" + message.Content
		}
	} else {
		value, err := strconv.Atoi(message.Content)
		if err != nil {
			err = app.WarnUserBadInput(message.ChannelID, verification)
			return err
		}
		profileURL = fmt.Sprintf("http://forum.sa-mp.com/member.php?u=%d", value)
	}

	verification.forumUser = profileURL
	verification.code, err = GenerateRandomString(8)
	if err != nil {
		return err
	}

	_, err = app.discordClient.ChannelMessageSend(
		message.ChannelID,
		fmt.Sprintf(
			`Thanks! Now you just need to paste this 8-digit verification code into your Bio section:

            **%s**

            You can edit this section here: http://forum.sa-mp.com/profile.php?do=editprofile in the 'Additional Information' section at the bottom.app`,
			verification.code),
	)
	return err
}

// UserConfirmsProfile is called when the user responds with 'done'
func (app App) UserConfirmsProfile(message discordgo.Message) error {
	var verification Verification
	var err error

	result, found := app.cache.Get(message.Author.ID)
	if !found {
		err = app.WarnUserNoVerification(message.ChannelID)
		return err
	}

	verification = result.(Verification)

	if verification.verifyState != VerificationStateAwaitConfirmation {
		err = app.WarnUserVerificationState(message.ChannelID, verification)
		return err
	}

	verified, err := app.CheckUserPageForCode(verification.forumUser, verification.code)
	if err != nil {
		return err
	}

	if verified {
		// store results
	} else {
		// could not verify
	}

	return nil
}

// UserCancelsVerification is called when the user responds with 'cancel'
func (app App) UserCancelsVerification(message discordgo.Message) error {
	return nil
}

// WarnUserVerificationState tells a user their current verification state,
// to be used when the user's reply does not match the expected reply according
// to the state of the Verification associated with the user.
func (app App) WarnUserVerificationState(channelid string, verification Verification) error {
	var stateMessage string
	switch verification.verifyState {
	case VerificationStateNone:
		stateMessage = "Your verification is currently in an invalid state, please try again in 5 minutes!"
	case VerificationStateAwaitProfileURL:
		stateMessage = "Your verification is currently awaiting a profile URL."
	case VerificationStateAwaitConfirmation:
		stateMessage = "Your verification is currently awaiting you to reply with either 'done' or 'cancel'"
	}
	_, err := app.discordClient.ChannelMessageSend(channelid, stateMessage)
	return err
}

// WarnUserNoVerification is simply a message informing the user their
// Verification does not exist and they need to start the process with 'verify'.
func (app App) WarnUserNoVerification(channelid string) error {
	_, err := app.discordClient.ChannelMessageSend(channelid, "You need to start your verification by typing 'verify'.")
	return err
}

// WarnUserBadInput lets the user know their input was not recognised for the
// current verification state.
func (app App) WarnUserBadInput(channelid string, verification Verification) error {
	var stateMessage string
	switch verification.verifyState {
	case VerificationStateNone:
		stateMessage = "Your verification is currently in an invalid state, please try again in 5 minutes!"
	case VerificationStateAwaitProfileURL:
		stateMessage = `Your input did not match the pattern for a valid profile URL or profile ID.
        The URL must be in one of the following formats:
        http://forum.sa-mp.com/member.php?u=50199
        forum.sa-mp.com/member.php?u=50199
        50199`
	case VerificationStateAwaitConfirmation:
		stateMessage = "Your verification is currently awaiting you to post the verification code on your Profile Bio, once you've done that reply with either 'done' or 'cancel'"
	}
	_, err := app.discordClient.ChannelMessageSend(channelid, stateMessage)
	return err
}

// WarnUserError informs the user of an error and provides them with
// instructions for what to do next.
func (app App) WarnUserError(channelid string, errorString string) error {
	_, err := app.discordClient.ChannelMessageSend(
		channelid,
		fmt.Sprintf(`
        An error occurred! A description of the error is below:

        %s

        Please let Southclaws know of this issue and try again in about 5 minutes`, errorString),
	)
	return err
}

// CheckUserPageForCode checks if a verification code has been posted by a user.
func (app App) CheckUserPageForCode(url string, code string) (bool, error) {
	var errors []error

	bio, err := app.GetUserBio(url)
	if err != nil {
		errors = append(errors, err)
	}

	if strings.Contains(bio, code) {
		return true, nil
	}

	visitorMessages, err := app.GetFirstTenUserVisitorMessages(url)
	if err != nil {
		errors = append(errors, err)
	}

	for _, m := range visitorMessages {
		if strings.Contains(m, code) {
			return true, nil
		}
	}

	return false, nil
}

/*
Author: Matt Silverlock
Date: 2014-05-24
Accessed: 2017-02-22
https://elithrar.github.io/article/generating-secure-random-numbers-crypto-rand
*/

// GenerateRandomBytes returns securely generated random bytes.
// It will return an error if the system's secure random
// number generator fails to function correctly, in which
// case the caller should not continue.
func GenerateRandomBytes(n int) ([]byte, error) {
	b := make([]byte, n)
	_, err := rand.Read(b)
	// Note that err == nil only if we read len(b) bytes.
	if err != nil {
		return nil, err
	}

	return b, nil
}

// GenerateRandomString returns a URL-safe, base64 encoded
// securely generated random string.
// It will return an error if the system's secure random
// number generator fails to function correctly, in which
// case the caller should not continue.
func GenerateRandomString(s int) (string, error) {
	b, err := GenerateRandomBytes(s)
	return base64.URLEncoding.EncodeToString(b), err
}