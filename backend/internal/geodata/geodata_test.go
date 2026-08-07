package geodata

import (
	"os"
	"testing"
)

func TestParseInstalledRulesDatCategories(t *testing.T) {
	geoip, err := os.ReadFile("/usr/share/ly-route/geodata/geoip.dat")
	if err != nil {
		t.Skip("installed geodata is not present in the unit-test environment")
	}
	ipData, err := ParseGeoIP(geoip, "CN")
	if err != nil || len(ipData.Entries) < 4000 {
		t.Fatalf("GeoIP CN parse = %d entries, err=%v", len(ipData.Entries), err)
	}
	geosite, err := os.ReadFile("/usr/share/ly-route/geodata/geosite.dat")
	if err != nil {
		t.Fatal(err)
	}
	siteData, err := ParseGeoSite(geosite, "CN")
	if err != nil || len(siteData.Entries) < 100000 {
		t.Fatalf("GeoSite CN parse = %d entries, err=%v", len(siteData.Entries), err)
	}
}
