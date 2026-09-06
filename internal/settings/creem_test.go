package settings

import (
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	model "github.com/tokenrouter/tokenrouter/internal/store"
)

func setupCreemSettingTest(t *testing.T) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+filepath.Join(t.TempDir(), "creem-settings.db")), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.Option{}))
	model.DB = db
	require.NoError(t, Init())
}

func TestCreemConfigurationValidationAndAtomicHotReload(t *testing.T) {
	setupCreemSettingTest(t)
	first := map[string]string{
		CreemAPIKeyOption:        "creem_key_one",
		CreemWebhookSecretOption: "creem_secret_one",
		CreemTestModeOption:      "true",
		CreemProductsOption:      `[{"productId":"prod_one","name":"One","price":12.34,"currency":"usd","quota":12345}]`,
	}
	require.NoError(t, UpdateOptions(first))
	config, err := GetCreemConfigChecked()
	require.NoError(t, err)
	assert.Equal(t, "creem_key_one", config.APIKey)
	assert.Equal(t, "creem_secret_one", config.WebhookSecret)
	assert.True(t, config.TestMode)
	require.Len(t, config.Products, 1)
	assert.Equal(t, "12.34", config.Products[0].PriceText)
	assert.Equal(t, "USD", config.Products[0].Currency)
	assert.True(t, CreemTopUpConfigured())

	for name, invalid := range map[string]map[string]string{
		"malformed products": {CreemProductsOption: `[{`},
		"null products":      {CreemProductsOption: `null`},
		"duplicate product": {CreemProductsOption: `[
			{"productId":"prod","name":"A","price":1,"currency":"USD","quota":1},
			{"productId":"prod","name":"B","price":2,"currency":"USD","quota":2}]`},
		"zero quota":        {CreemProductsOption: `[{"productId":"prod","name":"A","price":1,"currency":"USD","quota":0}]`},
		"exponent price":    {CreemProductsOption: `[{"productId":"prod","name":"A","price":1e2,"currency":"USD","quota":1}]`},
		"oversized price":   {CreemProductsOption: `[{"productId":"prod","name":"A","price":1000000,"currency":"USD","quota":1}]`},
		"invalid currency":  {CreemProductsOption: `[{"productId":"prod","name":"A","price":1,"currency":"US1","quota":1}]`},
		"invalid mode":      {CreemTestModeOption: "yes"},
		"spaced secret":     {CreemWebhookSecretOption: " secret"},
		"oversized catalog": {CreemProductsOption: strings.Repeat(" ", maxCreemProductsJSON) + "[]"},
	} {
		t.Run(name, func(t *testing.T) {
			require.Error(t, UpdateOptions(invalid))
			current, err := GetCreemConfigChecked()
			require.NoError(t, err)
			assert.Equal(t, "creem_key_one", current.APIKey)
			assert.Equal(t, "prod_one", current.Products[0].ProductID)
		})
	}

	second := map[string]string{
		CreemAPIKeyOption:        "creem_key_two",
		CreemWebhookSecretOption: "creem_secret_two",
		CreemTestModeOption:      "false",
		CreemProductsOption:      `[{"productId":"prod_two","name":"Two","price":5,"currency":"CNY","quota":50}]`,
	}
	start := make(chan struct{})
	seenInvalid := make(chan bool, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for range 500 {
				current, err := GetCreemConfigChecked()
				if err != nil || len(current.Products) != 1 {
					seenInvalid <- true
					return
				}
				productID := current.Products[0].ProductID
				old := current.APIKey == "creem_key_one" && current.WebhookSecret == "creem_secret_one" && current.TestMode && productID == "prod_one"
				newConfig := current.APIKey == "creem_key_two" && current.WebhookSecret == "creem_secret_two" && !current.TestMode && productID == "prod_two"
				if !old && !newConfig {
					seenInvalid <- true
					return
				}
			}
		}()
	}
	close(start)
	require.NoError(t, UpdateOptions(second))
	wg.Wait()
	close(seenInvalid)
	for invalid := range seenInvalid {
		assert.False(t, invalid)
	}
	config, err = GetCreemConfigChecked()
	require.NoError(t, err)
	assert.Equal(t, "creem_key_two", config.APIKey)
	assert.Equal(t, "prod_two", config.Products[0].ProductID)
}

func TestCreemConfigurationDefaultsRemainDisabled(t *testing.T) {
	setupCreemSettingTest(t)
	config, err := GetCreemConfigChecked()
	require.NoError(t, err)
	assert.Empty(t, config.APIKey)
	assert.Empty(t, config.WebhookSecret)
	assert.Equal(t, "[]", config.ProductsRaw)
	assert.Empty(t, config.Products)
	assert.False(t, config.TestMode)
	assert.False(t, CreemTopUpConfigured())
}
