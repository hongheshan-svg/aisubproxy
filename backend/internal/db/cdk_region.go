package db

// Old rows remain NULL until their current page is refreshed from CardPlatform.
// Treating NULL as PH would mislabel old codes issued for another region.
func migrateCardplatformCDKRegionCol() error {
	var n int
	if err := DB.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('cardplatform_cdk_codes') WHERE name='payment_country'`).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	_, err := DB.Exec(`ALTER TABLE cardplatform_cdk_codes ADD COLUMN payment_country TEXT`)
	return err
}
