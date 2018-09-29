package bot

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/bwmarrin/discordgo"
	"go.uber.org/zap"

	"github.com/Southclaws/cj/bot/asterisk"
	"github.com/Southclaws/cj/bot/commands"
	"github.com/Southclaws/cj/forum"
	"github.com/Southclaws/cj/storage"
	"github.com/Southclaws/cj/types"
)

// App stores program state
type App struct {
	config        *types.Config
	discordClient *discordgo.Session
	storage       *storage.API
	forum         *forum.ForumClient
	ready         chan error
	extensions    []Extension
}

// Extension represents an extension to the bot that receives a pointer to the
// storage backend.
type Extension interface {
	Init(*types.Config, *discordgo.Session, *storage.API, *forum.ForumClient) error
	OnMessage(discordgo.Message) error
}

// Start starts the app with the specified config and blocks until fatal error
func Start(config *types.Config) {
	app := App{
		config: config,
		ready:  make(chan error),
	}

	var err error

	app.storage, err = storage.New(storage.Config{
		MongoHost: config.MongoHost,
		MongoPort: config.MongoPort,
		MongoName: config.MongoName,
		MongoUser: config.MongoUser,
		MongoPass: config.MongoPass,
	})
	if err != nil {
		logger.Fatal("failed to connect to database", zap.Error(err))
	}

	app.forum, err = forum.NewForumClient()
	if err != nil {
		logger.Fatal("failed to initialise forum client", zap.Error(err))
	}

	err = app.ConnectDiscord()
	if err != nil {
		logger.Fatal("failed to connect to discord", zap.Error(err))
	}

	app.extensions = []Extension{
		&commands.CommandManager{},
		&asterisk.Asterisk{},
	}

	for _, ex := range app.extensions {
		err = ex.Init(config, app.discordClient, app.storage, app.forum)
		if err != nil {
			logger.Fatal("failed to initialise extension", zap.Error(err))
		}
	}

	app.forum.NewPostAlert("3", func() {
		//nolint:errcheck
		app.discordClient.ChannelMessageSend(
			config.PrimaryChannel,
			"New Kalcor Post: http://forum.sa-mp.com/search.php?do=finduser&u=3",
		)
	})

	_, err = app.discordClient.ChannelMessageSend(config.AdministrativeChannel, fmt.Sprintf("Hey, what's cracking now? Version %s", config.Version))
	if err != nil {
		logger.Fatal("failed to send initialisation message", zap.Error(err))
	}

	logger.Debug("started with debug logging enabled",
		zap.Int("extensions", len(app.extensions)),
		zap.Any("config", config))

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGTERM, syscall.SIGKILL)
	<-signals
}
