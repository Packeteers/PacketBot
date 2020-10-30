package main

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/andersfylling/disgord"
	"github.com/auttaja/gommand"
	"github.com/sirupsen/logrus"
	"github.com/spf13/viper"
)

var channelOptionRegex = regexp.MustCompile(`\[([^\[\]]*)\]`)

func expireMessages(client *disgord.Client) {
	// for each guild we're a part of
	for _, guildID := range client.GetConnectedGuilds() {
		guild, err := client.GetGuild(context.Background(), guildID)
		if err != nil {
			logrus.Error(err)
			continue
		}

		// for each channel in this guild
		channels, err := client.GetGuildChannels(context.Background(), guildID)
		if err != nil {
			logrus.Error(err)
			continue
		}
		for _, channel := range channels {
			var expireInDays int

			if channel.Type != disgord.ChannelTypeGuildText {
				// we only care about guild text channels
				continue
			}

			// look for pairs of square brackets in channel descriptions to look for "option pairs".
			// for example, "[expire: 7d]" where "expire" is the key, and "7d" is the value
			for _, element := range channelOptionRegex.FindAllString(channel.Topic, -1) {
				element = strings.Trim(element, "[]")
				options := strings.Split(element, ":")

				if len(options) != 2 {
					// not a valid key:value pair, move on
					continue
				}

				key := strings.Trim(options[0], " ")
				val := strings.Trim(options[1], " ")

				switch key {
				case "expire":
					if !strings.HasSuffix(val, "d") {
						logrus.Warnf("ignoring expire tag in %s/%s because it's not in Nd format", guild.Name, channel.Name)
						continue
					}
					val = strings.TrimSuffix(val, "d")
					expireInDays, err = strconv.Atoi(val)
					if err != nil {
						logrus.Warnf("ignoring expire tag in %s/%s because it's not an integer of days", guild.Name, channel.Name)
						continue
					}
					logrus.Debugf("[%s/#%s]: expire in %s days", guild.Name, channel.Name, val)
				}
			}

			if expireInDays == 0 {
				// this channel has no expire policy
				logrus.Debugf("[%s/#%s]: no expiry", guild.Name, channel.Name)
				continue
			}

			var messagesToDelete []*disgord.Message
			var earliestMessage disgord.Snowflake

			// try to load until the beginning of the channel, in case there are more than 100 "current" messages
			// this method seems wasteful and poorly optimized. this will need to be addressed
			for {
				// for each message in this channel
				messages, err := client.GetMessages(context.Background(), channel.ID, &disgord.GetMessagesParams{
					Limit:  100,
					Before: earliestMessage,
				})
				if err != nil {
					logrus.Error(err)
					break
				}
				if len(messages) == 0 {
					// we've reached the beginning of this channel
					logrus.Infof("[%s/%s] reached the beginning with %d to remove", guild.Name, channel.Name, len(messagesToDelete))
					break
				}
				for _, message := range messages {
					earliestMessage = message.ID

					if message.Pinned {
						// ignore pinned messages
						continue
					}
					if message.Timestamp.Before(time.Now().Add(-time.Duration(expireInDays) * 24 * time.Hour)) {
						messagesToDelete = append(messagesToDelete, message)
					}
					if len(messagesToDelete) >= 5 {
						logrus.Debugf("[%s/%s] more than 5 messages to delete, will come back later", guild.Name, channel.Name)
						break
					}
				}
				if len(messagesToDelete) >= 5 {
					logrus.Debugf("[%s/%s] more than 5 messages to delete, will come back later", guild.Name, channel.Name)
					break
				}
			}

			for _, message := range messagesToDelete {
				// TODO: probably make this a debug log call
				logrus.Infof("[%s/%s] older than %dd: %s", guild.Name, channel.Name, expireInDays, message.Content)
				if err := client.DeleteMessage(context.Background(), channel.ID, message.ID); err != nil {
					logrus.Error(err)
					continue
				}
			}
		}
	}
}

func main() {
	logrus.Info("startup")
	logrus.SetLevel(logrus.DebugLevel)

	viper.SetConfigName("packetbot")
	viper.AddConfigPath(".")
	err := viper.ReadInConfig()
	if err != nil {
		panic(fmt.Errorf("config: %s", err))
	}

	disgordLogger := logrus.New()
	disgordLogger.SetLevel(logrus.DebugLevel)

	client := disgord.New(disgord.Config{
		BotToken: viper.GetString("token"),
		//RejectEvents: disgord.AllEventsExcept(disgord.EvtMessageCreate),
		Logger: disgordLogger,
	})
	defer client.StayConnectedUntilInterrupted(context.Background())

	// set up commands
	router := gommand.NewRouter(&gommand.RouterConfig{
		PrefixCheck: gommand.MentionPrefix,
	})
	router.Hook(client)

	// set up message expiry timer
	client.Ready(func() {
		go func() {
			for range time.Tick(15 * time.Second) {
				logrus.Debug("running expiry pass")
				expireMessages(client)
			}
		}()
	})
}
