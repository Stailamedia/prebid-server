package exchange

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"testing"

	"github.com/prebid/openrtb/v17/openrtb2"
	"github.com/prebid/prebid-server/config"
	"github.com/prebid/prebid-server/errortypes"
	"github.com/prebid/prebid-server/firstpartydata"
	"github.com/prebid/prebid-server/gdpr"
	"github.com/prebid/prebid-server/metrics"
	"github.com/prebid/prebid-server/openrtb_ext"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

// permissionsMock mocks the Permissions interface for tests
type permissionsMock struct {
	allowAllBidders bool
	allowedBidders  []openrtb_ext.BidderName
	passGeo         bool
	passID          bool
	activitiesError error
}

func (p *permissionsMock) HostCookiesAllowed(ctx context.Context) (bool, error) {
	return true, nil
}

func (p *permissionsMock) BidderSyncAllowed(ctx context.Context, bidder openrtb_ext.BidderName) (bool, error) {
	return true, nil
}

func (p *permissionsMock) AuctionActivitiesAllowed(ctx context.Context, bidderCoreName openrtb_ext.BidderName, bidder openrtb_ext.BidderName) (permissions gdpr.AuctionPermissions, err error) {
	permissions = gdpr.AuctionPermissions{
		PassGeo: p.passGeo,
		PassID:  p.passID,
	}

	if p.allowAllBidders {
		permissions.AllowBidRequest = true
		return permissions, p.activitiesError
	}

	for _, allowedBidder := range p.allowedBidders {
		if bidder == allowedBidder {
			permissions.AllowBidRequest = true
		}
	}

	return permissions, p.activitiesError
}

type fakePermissionsBuilder struct {
	permissions gdpr.Permissions
}

func (fpb fakePermissionsBuilder) Builder(gdpr.TCF2ConfigReader, gdpr.RequestInfo) gdpr.Permissions {
	return fpb.permissions
}

type fakeTCF2ConfigBuilder struct {
	cfg gdpr.TCF2ConfigReader
}

func (fcr fakeTCF2ConfigBuilder) Builder(hostConfig config.TCF2, accountConfig config.AccountGDPR) gdpr.TCF2ConfigReader {
	return fcr.cfg
}

func assertReq(t *testing.T, bidderRequests []BidderRequest,
	applyCOPPA bool, consentedVendors map[string]bool) {
	// assert individual bidder requests
	assert.NotEqual(t, bidderRequests, 0, "cleanOpenRTBRequest should split request into individual bidder requests")

	// assert for PI data
	// Both appnexus and brightroll should be allowed since brightroll
	// is used as an alias for appnexus in the test request
	for _, req := range bidderRequests {
		if !applyCOPPA && consentedVendors[req.BidderName.String()] {
			assert.NotEqual(t, req.BidRequest.User.BuyerUID, "", "cleanOpenRTBRequest shouldn't clean PI data as per COPPA or for a consented vendor as per GDPR or per CCPA")
			assert.NotEqual(t, req.BidRequest.Device.DIDMD5, "", "cleanOpenRTBRequest shouldn't clean PI data as per COPPA or for a consented vendor as per GDPR or per CCPA")
		} else {
			assert.Equal(t, req.BidRequest.User.BuyerUID, "", "cleanOpenRTBRequest should clean PI data as per COPPA or for a non-consented vendor as per GDPR or per CCPA", req.BidderName.String())
			assert.Equal(t, req.BidRequest.Device.DIDMD5, "", "cleanOpenRTBRequest should clean PI data as per COPPA or for a non-consented vendor as per GDPR or per CCPA", req.BidderName.String())
		}
	}
}

func TestSplitImps(t *testing.T) {
	testCases := []struct {
		description   string
		givenImps     []openrtb2.Imp
		expectedImps  map[string][]openrtb2.Imp
		expectedError string
	}{
		{
			description:   "Nil",
			givenImps:     nil,
			expectedImps:  map[string][]openrtb2.Imp{},
			expectedError: "",
		},
		{
			description:   "Empty",
			givenImps:     []openrtb2.Imp{},
			expectedImps:  map[string][]openrtb2.Imp{},
			expectedError: "",
		},
		{
			description: "1 Imp, 1 Bidder",
			givenImps: []openrtb2.Imp{
				{ID: "imp1", Ext: json.RawMessage(`{"prebid":{"bidder":{"bidderA":{"imp1ParamA":"imp1ValueA"}}}}`)},
			},
			expectedImps: map[string][]openrtb2.Imp{
				"bidderA": {
					{ID: "imp1", Ext: json.RawMessage(`{"bidder":{"imp1ParamA":"imp1ValueA"}}`)},
				},
			},
			expectedError: "",
		},
		{
			description: "1 Imp, 2 Bidders",
			givenImps: []openrtb2.Imp{
				{ID: "imp1", Ext: json.RawMessage(`{"prebid":{"bidder":{"bidderA":{"imp1ParamA":"imp1ValueA"},"bidderB":{"imp1ParamB":"imp1ValueB"}}}}`)},
			},
			expectedImps: map[string][]openrtb2.Imp{
				"bidderA": {
					{ID: "imp1", Ext: json.RawMessage(`{"bidder":{"imp1ParamA":"imp1ValueA"}}`)},
				},
				"bidderB": {
					{ID: "imp1", Ext: json.RawMessage(`{"bidder":{"imp1ParamB":"imp1ValueB"}}`)},
				},
			},
			expectedError: "",
		},
		{
			description: "2 Imps, 1 Bidders Each",
			givenImps: []openrtb2.Imp{
				{ID: "imp1", Ext: json.RawMessage(`{"prebid":{"bidder":{"bidderA":{"imp1ParamA":"imp1ValueA"}}}}`)},
				{ID: "imp2", Ext: json.RawMessage(`{"prebid":{"bidder":{"bidderA":{"imp2ParamA":"imp2ValueA"}}}}`)},
			},
			expectedImps: map[string][]openrtb2.Imp{
				"bidderA": {
					{ID: "imp1", Ext: json.RawMessage(`{"bidder":{"imp1ParamA":"imp1ValueA"}}`)},
					{ID: "imp2", Ext: json.RawMessage(`{"bidder":{"imp2ParamA":"imp2ValueA"}}`)},
				},
			},
			expectedError: "",
		},
		{
			description: "2 Imps, 2 Bidders Each",
			givenImps: []openrtb2.Imp{
				{ID: "imp1", Ext: json.RawMessage(`{"prebid":{"bidder":{"bidderA":{"imp1paramA":"imp1valueA"},"bidderB":{"imp1paramB":"imp1valueB"}}}}`)},
				{ID: "imp2", Ext: json.RawMessage(`{"prebid":{"bidder":{"bidderA":{"imp2paramA":"imp2valueA"},"bidderB":{"imp2paramB":"imp2valueB"}}}}`)},
			},
			expectedImps: map[string][]openrtb2.Imp{
				"bidderA": {
					{ID: "imp1", Ext: json.RawMessage(`{"bidder":{"imp1paramA":"imp1valueA"}}`)},
					{ID: "imp2", Ext: json.RawMessage(`{"bidder":{"imp2paramA":"imp2valueA"}}`)},
				},
				"bidderB": {
					{ID: "imp1", Ext: json.RawMessage(`{"bidder":{"imp1paramB":"imp1valueB"}}`)},
					{ID: "imp2", Ext: json.RawMessage(`{"bidder":{"imp2paramB":"imp2valueB"}}`)},
				},
			},
			expectedError: "",
		},
		{
			// This is a "happy path" integration test. Functionality is covered in detail by TestCreateSanitizedImpExt.
			description: "Other Fields - 2 Imps, 2 Bidders Each",
			givenImps: []openrtb2.Imp{
				{ID: "imp1", Ext: json.RawMessage(`{"prebid":{"bidder":{"bidderA":{"imp1paramA":"imp1valueA"},"bidderB":{"imp1paramB":"imp1valueB"}}},"skadn":"imp1SkAdN"}`)},
				{ID: "imp2", Ext: json.RawMessage(`{"prebid":{"bidder":{"bidderA":{"imp2paramA":"imp2valueA"},"bidderB":{"imp2paramB":"imp2valueB"}}},"skadn":"imp2SkAdN"}`)},
			},
			expectedImps: map[string][]openrtb2.Imp{
				"bidderA": {
					{ID: "imp1", Ext: json.RawMessage(`{"bidder":{"imp1paramA":"imp1valueA"},"skadn":"imp1SkAdN"}`)},
					{ID: "imp2", Ext: json.RawMessage(`{"bidder":{"imp2paramA":"imp2valueA"},"skadn":"imp2SkAdN"}`)},
				},
				"bidderB": {
					{ID: "imp1", Ext: json.RawMessage(`{"bidder":{"imp1paramB":"imp1valueB"},"skadn":"imp1SkAdN"}`)},
					{ID: "imp2", Ext: json.RawMessage(`{"bidder":{"imp2paramB":"imp2valueB"},"skadn":"imp2SkAdN"}`)},
				},
			},
			expectedError: "",
		},
		{
			description: "Malformed imp.ext",
			givenImps: []openrtb2.Imp{
				{ID: "imp1", Ext: json.RawMessage(`malformed`)},
			},
			expectedError: "invalid json for imp[0]: invalid character 'm' looking for beginning of value",
		},
		{
			description: "Malformed imp.ext.prebid",
			givenImps: []openrtb2.Imp{
				{ID: "imp1", Ext: json.RawMessage(`{"prebid": malformed}`)},
			},
			expectedError: "invalid json for imp[0]: invalid character 'm' looking for beginning of value",
		},
		{
			description: "Malformed imp.ext.prebid.bidder",
			givenImps: []openrtb2.Imp{
				{ID: "imp1", Ext: json.RawMessage(`{"prebid": {"bidder": malformed}}`)},
			},
			expectedError: "invalid json for imp[0]: invalid character 'm' looking for beginning of value",
		},
	}

	for _, test := range testCases {
		imps, err := splitImps(test.givenImps)

		if test.expectedError == "" {
			assert.NoError(t, err, test.description+":err")
		} else {
			assert.EqualError(t, err, test.expectedError, test.description+":err")
		}

		assert.Equal(t, test.expectedImps, imps, test.description+":imps")
	}
}

func TestCreateSanitizedImpExt(t *testing.T) {
	testCases := []struct {
		description       string
		givenImpExt       map[string]json.RawMessage
		givenImpExtPrebid map[string]json.RawMessage
		expected          map[string]json.RawMessage
		expectedError     string
	}{
		{
			description:       "Nil",
			givenImpExt:       nil,
			givenImpExtPrebid: nil,
			expected:          map[string]json.RawMessage{},
			expectedError:     "",
		},
		{
			description:       "Empty",
			givenImpExt:       map[string]json.RawMessage{},
			givenImpExtPrebid: map[string]json.RawMessage{},
			expected:          map[string]json.RawMessage{},
			expectedError:     "",
		},
		{
			description: "imp.ext.prebid - Bidder Only",
			givenImpExt: map[string]json.RawMessage{
				"prebid":  json.RawMessage(`"ignoredInFavorOfSeparatelyUnmarshalledImpExtPrebid"`),
				"data":    json.RawMessage(`"anyData"`),
				"context": json.RawMessage(`"anyContext"`),
				"skadn":   json.RawMessage(`"anySKAdNetwork"`),
				"gpid":    json.RawMessage(`"anyGPID"`),
				"tid":     json.RawMessage(`"anyTID"`),
			},
			givenImpExtPrebid: map[string]json.RawMessage{
				"bidder": json.RawMessage(`"anyBidder"`),
			},
			expected: map[string]json.RawMessage{
				"data":    json.RawMessage(`"anyData"`),
				"context": json.RawMessage(`"anyContext"`),
				"skadn":   json.RawMessage(`"anySKAdNetwork"`),
				"gpid":    json.RawMessage(`"anyGPID"`),
				"tid":     json.RawMessage(`"anyTID"`),
			},
			expectedError: "",
		},
		{
			description: "imp.ext.prebid - Bidder + Other Forbidden Value",
			givenImpExt: map[string]json.RawMessage{
				"prebid":  json.RawMessage(`"ignoredInFavorOfSeparatelyUnmarshalledImpExtPrebid"`),
				"data":    json.RawMessage(`"anyData"`),
				"context": json.RawMessage(`"anyContext"`),
				"skadn":   json.RawMessage(`"anySKAdNetwork"`),
				"gpid":    json.RawMessage(`"anyGPID"`),
				"tid":     json.RawMessage(`"anyTID"`),
			},
			givenImpExtPrebid: map[string]json.RawMessage{
				"bidder":    json.RawMessage(`"anyBidder"`),
				"forbidden": json.RawMessage(`"anyValue"`),
			},
			expected: map[string]json.RawMessage{
				"data":    json.RawMessage(`"anyData"`),
				"context": json.RawMessage(`"anyContext"`),
				"skadn":   json.RawMessage(`"anySKAdNetwork"`),
				"gpid":    json.RawMessage(`"anyGPID"`),
				"tid":     json.RawMessage(`"anyTID"`),
			},
			expectedError: "",
		},
		{
			description: "imp.ext.prebid - Bidder + Other Allowed Values",
			givenImpExt: map[string]json.RawMessage{
				"prebid":  json.RawMessage(`"ignoredInFavorOfSeparatelyUnmarshalledImpExtPrebid"`),
				"data":    json.RawMessage(`"anyData"`),
				"context": json.RawMessage(`"anyContext"`),
				"skadn":   json.RawMessage(`"anySKAdNetwork"`),
				"gpid":    json.RawMessage(`"anyGPID"`),
				"tid":     json.RawMessage(`"anyTID"`),
			},
			givenImpExtPrebid: map[string]json.RawMessage{
				"bidder":                json.RawMessage(`"anyBidder"`),
				"is_rewarded_inventory": json.RawMessage(`"anyIsRewardedInventory"`),
				"options":               json.RawMessage(`"anyOptions"`),
			},
			expected: map[string]json.RawMessage{
				"prebid":  json.RawMessage(`{"is_rewarded_inventory":"anyIsRewardedInventory","options":"anyOptions"}`),
				"data":    json.RawMessage(`"anyData"`),
				"context": json.RawMessage(`"anyContext"`),
				"skadn":   json.RawMessage(`"anySKAdNetwork"`),
				"gpid":    json.RawMessage(`"anyGPID"`),
				"tid":     json.RawMessage(`"anyTID"`),
			},
			expectedError: "",
		},
		{
			description: "imp.ext",
			givenImpExt: map[string]json.RawMessage{
				"anyBidder": json.RawMessage(`"anyBidderValues"`),
				"data":      json.RawMessage(`"anyData"`),
				"context":   json.RawMessage(`"anyContext"`),
				"skadn":     json.RawMessage(`"anySKAdNetwork"`),
				"gpid":      json.RawMessage(`"anyGPID"`),
				"tid":       json.RawMessage(`"anyTID"`),
			},
			givenImpExtPrebid: map[string]json.RawMessage{},
			expected: map[string]json.RawMessage{
				"data":    json.RawMessage(`"anyData"`),
				"context": json.RawMessage(`"anyContext"`),
				"skadn":   json.RawMessage(`"anySKAdNetwork"`),
				"gpid":    json.RawMessage(`"anyGPID"`),
				"tid":     json.RawMessage(`"anyTID"`),
			},
			expectedError: "",
		},
		{
			description: "imp.ext + imp.ext.prebid - Prebid Bidder Only",
			givenImpExt: map[string]json.RawMessage{
				"anyBidder": json.RawMessage(`"anyBidderValues"`),
				"prebid":    json.RawMessage(`"ignoredInFavorOfSeparatelyUnmarshalledImpExtPrebid"`),
				"data":      json.RawMessage(`"anyData"`),
				"context":   json.RawMessage(`"anyContext"`),
				"skadn":     json.RawMessage(`"anySKAdNetwork"`),
				"gpid":      json.RawMessage(`"anyGPID"`),
				"tid":       json.RawMessage(`"anyTID"`),
			},
			givenImpExtPrebid: map[string]json.RawMessage{
				"bidder": json.RawMessage(`"anyBidder"`),
			},
			expected: map[string]json.RawMessage{
				"data":    json.RawMessage(`"anyData"`),
				"context": json.RawMessage(`"anyContext"`),
				"skadn":   json.RawMessage(`"anySKAdNetwork"`),
				"gpid":    json.RawMessage(`"anyGPID"`),
				"tid":     json.RawMessage(`"anyTID"`),
			},
			expectedError: "",
		},
		{
			description: "imp.ext + imp.ext.prebid - Prebid Bidder + Other Forbidden Value",
			givenImpExt: map[string]json.RawMessage{
				"anyBidder": json.RawMessage(`"anyBidderValues"`),
				"prebid":    json.RawMessage(`"ignoredInFavorOfSeparatelyUnmarshalledImpExtPrebid"`),
				"data":      json.RawMessage(`"anyData"`),
				"context":   json.RawMessage(`"anyContext"`),
				"skadn":     json.RawMessage(`"anySKAdNetwork"`),
				"gpid":      json.RawMessage(`"anyGPID"`),
				"tid":       json.RawMessage(`"anyTID"`),
			},
			givenImpExtPrebid: map[string]json.RawMessage{
				"bidder":    json.RawMessage(`"anyBidder"`),
				"forbidden": json.RawMessage(`"anyValue"`),
			},
			expected: map[string]json.RawMessage{
				"data":    json.RawMessage(`"anyData"`),
				"context": json.RawMessage(`"anyContext"`),
				"skadn":   json.RawMessage(`"anySKAdNetwork"`),
				"gpid":    json.RawMessage(`"anyGPID"`),
				"tid":     json.RawMessage(`"anyTID"`),
			},
			expectedError: "",
		},
		{
			description: "imp.ext + imp.ext.prebid - Prebid Bidder + Other Allowed Values",
			givenImpExt: map[string]json.RawMessage{
				"anyBidder": json.RawMessage(`"anyBidderValues"`),
				"prebid":    json.RawMessage(`"ignoredInFavorOfSeparatelyUnmarshalledImpExtPrebid"`),
				"data":      json.RawMessage(`"anyData"`),
				"context":   json.RawMessage(`"anyContext"`),
				"skadn":     json.RawMessage(`"anySKAdNetwork"`),
				"gpid":      json.RawMessage(`"anyGPID"`),
				"tid":       json.RawMessage(`"anyTID"`),
			},
			givenImpExtPrebid: map[string]json.RawMessage{
				"bidder":                json.RawMessage(`"anyBidder"`),
				"is_rewarded_inventory": json.RawMessage(`"anyIsRewardedInventory"`),
				"options":               json.RawMessage(`"anyOptions"`),
			},
			expected: map[string]json.RawMessage{
				"prebid":  json.RawMessage(`{"is_rewarded_inventory":"anyIsRewardedInventory","options":"anyOptions"}`),
				"data":    json.RawMessage(`"anyData"`),
				"context": json.RawMessage(`"anyContext"`),
				"skadn":   json.RawMessage(`"anySKAdNetwork"`),
				"gpid":    json.RawMessage(`"anyGPID"`),
				"tid":     json.RawMessage(`"anyTID"`),
			},
			expectedError: "",
		},
		{
			description: "Marshal Error - imp.ext.prebid",
			givenImpExt: map[string]json.RawMessage{
				"prebid":  json.RawMessage(`"ignoredInFavorOfSeparatelyUnmarshalledImpExtPrebid"`),
				"data":    json.RawMessage(`"anyData"`),
				"context": json.RawMessage(`"anyContext"`),
				"skadn":   json.RawMessage(`"anySKAdNetwork"`),
				"gpid":    json.RawMessage(`"anyGPID"`),
				"tid":     json.RawMessage(`"anyTID"`),
			},
			givenImpExtPrebid: map[string]json.RawMessage{
				"options": json.RawMessage(`malformed`), // String value without quotes.
			},
			expected:      nil,
			expectedError: "cannot marshal ext.prebid: json: error calling MarshalJSON for type json.RawMessage: invalid character 'm' looking for beginning of value",
		},
	}

	for _, test := range testCases {
		result, err := createSanitizedImpExt(test.givenImpExt, test.givenImpExtPrebid)

		if test.expectedError == "" {
			assert.NoError(t, err, test.description+":err")
		} else {
			assert.EqualError(t, err, test.expectedError, test.description+":err")
		}

		assert.Equal(t, test.expected, result, test.description)
	}
}

func TestCleanOpenRTBRequests(t *testing.T) {
	testCases := []struct {
		req              AuctionRequest
		bidReqAssertions func(t *testing.T, bidderRequests []BidderRequest,
			applyCOPPA bool, consentedVendors map[string]bool)
		hasError         bool
		applyCOPPA       bool
		consentedVendors map[string]bool
	}{
		{
			req:              AuctionRequest{BidRequestWrapper: &openrtb_ext.RequestWrapper{BidRequest: getTestBuildRequest(t)}, UserSyncs: &emptyUsersync{}},
			bidReqAssertions: assertReq,
			hasError:         false,
			applyCOPPA:       true,
			consentedVendors: map[string]bool{"appnexus": true},
		},
		{
			req:              AuctionRequest{BidRequestWrapper: &openrtb_ext.RequestWrapper{BidRequest: newAdapterAliasBidRequest(t)}, UserSyncs: &emptyUsersync{}},
			bidReqAssertions: assertReq,
			hasError:         false,
			applyCOPPA:       false,
			consentedVendors: map[string]bool{"appnexus": true, "brightroll": true},
		},
	}

	privacyConfig := config.Privacy{
		CCPA: config.CCPA{
			Enforce: true,
		},
		LMT: config.LMT{
			Enforce: true,
		},
	}

	for _, test := range testCases {

		gdprPermsBuilder := fakePermissionsBuilder{
			permissions: &permissionsMock{
				allowAllBidders: true,
			},
		}.Builder
		tcf2ConfigBuilder := fakeTCF2ConfigBuilder{
			cfg: gdpr.NewTCF2Config(config.TCF2{}, config.AccountGDPR{}),
		}.Builder

		reqSplitter := &requestSplitter{
			bidderToSyncerKey: map[string]string{},
			me:                &metrics.MetricsEngineMock{},
			privacyConfig:     privacyConfig,
			gdprPermsBuilder:  gdprPermsBuilder,
			tcf2ConfigBuilder: tcf2ConfigBuilder,
			hostSChainNode:    nil,
			bidderInfo:        config.BidderInfos{},
		}
		bidderRequests, _, err := reqSplitter.cleanOpenRTBRequests(context.Background(), test.req, nil, gdpr.SignalNo)
		if test.hasError {
			assert.NotNil(t, err, "Error shouldn't be nil")
		} else {
			assert.Nil(t, err, "Err should be nil")
			test.bidReqAssertions(t, bidderRequests, test.applyCOPPA, test.consentedVendors)
		}
	}
}

func TestCleanOpenRTBRequestsWithFPD(t *testing.T) {
	fpd := make(map[openrtb_ext.BidderName]*firstpartydata.ResolvedFirstPartyData)

	apnFpd := firstpartydata.ResolvedFirstPartyData{
		Site: &openrtb2.Site{Name: "fpdApnSite"},
		App:  &openrtb2.App{Name: "fpdApnApp"},
		User: &openrtb2.User{Keywords: "fpdApnUser"},
	}
	fpd[openrtb_ext.BidderName("appnexus")] = &apnFpd

	brightrollFpd := firstpartydata.ResolvedFirstPartyData{
		Site: &openrtb2.Site{Name: "fpdBrightrollSite"},
		App:  &openrtb2.App{Name: "fpdBrightrollApp"},
		User: &openrtb2.User{Keywords: "fpdBrightrollUser"},
	}
	fpd[openrtb_ext.BidderName("brightroll")] = &brightrollFpd

	testCases := []struct {
		description string
		req         AuctionRequest
		fpdExpected bool
	}{
		{
			description: "Pass valid FPD data for bidder not found in the request",
			req:         AuctionRequest{BidRequestWrapper: &openrtb_ext.RequestWrapper{BidRequest: getTestBuildRequest(t)}, UserSyncs: &emptyUsersync{}, FirstPartyData: fpd},
			fpdExpected: false,
		},
		{
			description: "Pass valid FPD data for bidders specified in request",
			req:         AuctionRequest{BidRequestWrapper: &openrtb_ext.RequestWrapper{BidRequest: newAdapterAliasBidRequest(t)}, UserSyncs: &emptyUsersync{}, FirstPartyData: fpd},
			fpdExpected: true,
		},
		{
			description: "Bidders specified in request but there is no fpd data for this bidder",
			req:         AuctionRequest{BidRequestWrapper: &openrtb_ext.RequestWrapper{BidRequest: newAdapterAliasBidRequest(t)}, UserSyncs: &emptyUsersync{}, FirstPartyData: make(map[openrtb_ext.BidderName]*firstpartydata.ResolvedFirstPartyData)},
			fpdExpected: false,
		},
		{
			description: "No FPD data passed",
			req:         AuctionRequest{BidRequestWrapper: &openrtb_ext.RequestWrapper{BidRequest: newAdapterAliasBidRequest(t)}, UserSyncs: &emptyUsersync{}, FirstPartyData: nil},
			fpdExpected: false,
		},
	}

	for _, test := range testCases {

		gdprPermissionsBuilder := fakePermissionsBuilder{
			permissions: &permissionsMock{
				allowAllBidders: true,
			},
		}.Builder
		tcf2ConfigBuilder := fakeTCF2ConfigBuilder{
			cfg: gdpr.NewTCF2Config(config.TCF2{}, config.AccountGDPR{}),
		}.Builder

		reqSplitter := &requestSplitter{
			bidderToSyncerKey: map[string]string{},
			me:                &metrics.MetricsEngineMock{},
			privacyConfig:     config.Privacy{},
			gdprPermsBuilder:  gdprPermissionsBuilder,
			tcf2ConfigBuilder: tcf2ConfigBuilder,
			hostSChainNode:    nil,
			bidderInfo:        config.BidderInfos{},
		}

		bidderRequests, _, err := reqSplitter.cleanOpenRTBRequests(context.Background(), test.req, nil, gdpr.SignalNo)
		assert.Empty(t, err, "No errors should be returned")
		for _, bidderRequest := range bidderRequests {
			bidderName := bidderRequest.BidderName
			if test.fpdExpected {
				assert.Equal(t, fpd[bidderName].Site.Name, bidderRequest.BidRequest.Site.Name, "Incorrect FPD site name")
				assert.Equal(t, fpd[bidderName].App.Name, bidderRequest.BidRequest.App.Name, "Incorrect FPD app name")
				assert.Equal(t, fpd[bidderName].User.Keywords, bidderRequest.BidRequest.User.Keywords, "Incorrect FPD user keywords")
				assert.Equal(t, test.req.BidRequestWrapper.User.BuyerUID, bidderRequest.BidRequest.User.BuyerUID, "Incorrect FPD user buyerUID")
			} else {
				assert.Equal(t, "", bidderRequest.BidRequest.Site.Name, "Incorrect FPD site name")
				assert.Equal(t, "", bidderRequest.BidRequest.User.Keywords, "Incorrect FPD user keywords")
			}
		}
	}
}

func TestCleanOpenRTBRequestsWithBidResponses(t *testing.T) {
	bidRespId1 := json.RawMessage(`{"id": "resp_id1"}`)
	bidRespId2 := json.RawMessage(`{"id": "resp_id2"}`)

	testCases := []struct {
		description            string
		storedBidResponses     map[string]map[string]json.RawMessage
		imps                   []openrtb2.Imp
		expectedBidderRequests map[string]BidderRequest
	}{
		{
			description: "Request with imp with one bidder stored bid response",
			storedBidResponses: map[string]map[string]json.RawMessage{
				"imp-id1": {"bidderA": bidRespId1},
			},
			imps: []openrtb2.Imp{
				{
					ID: "imp-id1",
					Video: &openrtb2.Video{
						W: 300,
						H: 250,
					},
					Ext: json.RawMessage(`{"prebid":{"bidder":{"bidderA":{"placementId":"123"}}}}`),
				},
			},
			expectedBidderRequests: map[string]BidderRequest{
				"bidderA": {
					BidRequest: &openrtb2.BidRequest{Imp: nil},
					BidderName: "bidderA",
					BidderStoredResponses: map[string]json.RawMessage{
						"imp-id1": bidRespId1},
				},
			},
		},
		{
			description: "Request with imps with and without stored bid response for one bidder",
			storedBidResponses: map[string]map[string]json.RawMessage{
				"imp-id1": {"bidderA": bidRespId1},
			},
			imps: []openrtb2.Imp{
				{
					ID: "imp-id1",
					Video: &openrtb2.Video{
						W: 300,
						H: 250,
					},
					Ext: json.RawMessage(`{"prebid":{"bidder":{"bidderA":{"placementId":"123"}}}}`),
				},
				{
					ID:  "imp-id2",
					Ext: json.RawMessage(`{"prebid":{"bidder":{"bidderA":{"placementId":"123"}}}}`),
				},
			},
			expectedBidderRequests: map[string]BidderRequest{
				"bidderA": {
					BidRequest: &openrtb2.BidRequest{Imp: []openrtb2.Imp{
						{ID: "imp-id2", Ext: json.RawMessage(`{"bidder":{"placementId":"123"}}`)},
					}},
					BidderName: "bidderA",
					BidderStoredResponses: map[string]json.RawMessage{
						"imp-id1": bidRespId1},
				},
			},
		},
		{
			description: "Request with imp with 2 bidders stored bid response",
			storedBidResponses: map[string]map[string]json.RawMessage{
				"imp-id1": {"bidderA": bidRespId1, "bidderB": bidRespId2},
			},
			imps: []openrtb2.Imp{
				{
					ID: "imp-id1",
					Video: &openrtb2.Video{
						W: 300,
						H: 250,
					},
					Ext: json.RawMessage(`{"prebid":{"bidder":{"bidderA":{"placementId":"123"}}}}`),
				},
			},
			expectedBidderRequests: map[string]BidderRequest{
				"bidderA": {
					BidRequest: &openrtb2.BidRequest{Imp: nil},
					BidderName: "bidderA",
					BidderStoredResponses: map[string]json.RawMessage{
						"imp-id1": bidRespId1,
					},
				},
				"bidderB": {
					BidRequest: &openrtb2.BidRequest{Imp: nil},
					BidderName: "bidderB",
					BidderStoredResponses: map[string]json.RawMessage{
						"imp-id1": bidRespId2},
				},
			},
		},
		{
			description: "Request with 2 imps: with 2 bidders stored bid response and imp without stored responses",
			storedBidResponses: map[string]map[string]json.RawMessage{
				"imp-id1": {"bidderA": bidRespId1, "bidderB": bidRespId2},
			},
			imps: []openrtb2.Imp{
				{
					ID: "imp-id1",
					Video: &openrtb2.Video{
						W: 300,
						H: 250,
					},
					Ext: json.RawMessage(`{"prebid":{"bidder":{"bidderA":{"placementId":"123"}}}}`),
				},
				{
					ID:  "imp-id2",
					Ext: json.RawMessage(`{"prebid":{"bidder":{"bidderA":{"placementId":"123"}}}}`),
				},
			},
			expectedBidderRequests: map[string]BidderRequest{
				"bidderA": {
					BidRequest: &openrtb2.BidRequest{Imp: []openrtb2.Imp{
						{ID: "imp-id2", Ext: json.RawMessage(`{"bidder":{"placementId":"123"}}`)},
					}},
					BidderName: "bidderA",
					BidderStoredResponses: map[string]json.RawMessage{
						"imp-id1": bidRespId1},
				},
				"bidderB": {
					BidRequest: &openrtb2.BidRequest{Imp: nil},
					BidderName: "bidderB",
					BidderStoredResponses: map[string]json.RawMessage{
						"imp-id1": bidRespId2},
				},
			},
		},
		{
			description: "Request with 3 imps: with 2 bidders stored bid response and 2 imps without stored responses",
			storedBidResponses: map[string]map[string]json.RawMessage{
				"imp-id1": {"bidderA": bidRespId1, "bidderB": bidRespId2},
			},
			imps: []openrtb2.Imp{
				{
					ID:  "imp-id3",
					Ext: json.RawMessage(`{"prebid":{"bidder":{"bidderC":{"placementId":"1234"}}}}`),
				},
				{
					ID: "imp-id1",
					Video: &openrtb2.Video{
						W: 300,
						H: 250,
					},
					Ext: json.RawMessage(`{"prebid":{"bidder":{"bidderA":{"placementId":"123"}}}}`),
				},
				{
					ID:  "imp-id2",
					Ext: json.RawMessage(`{"prebid":{"bidder":{"bidderA":{"placementId":"123"}}}}`),
				},
			},
			expectedBidderRequests: map[string]BidderRequest{
				"bidderA": {
					BidRequest: &openrtb2.BidRequest{Imp: []openrtb2.Imp{
						{ID: "imp-id2", Ext: json.RawMessage(`{"bidder":{"placementId":"123"}}`)},
					}},
					BidderName: "bidderA",
					BidderStoredResponses: map[string]json.RawMessage{
						"imp-id1": bidRespId1},
				},
				"bidderB": {
					BidRequest: &openrtb2.BidRequest{Imp: nil},
					BidderName: "bidderB",
					BidderStoredResponses: map[string]json.RawMessage{
						"imp-id1": bidRespId2},
				},
				"bidderC": {
					BidRequest: &openrtb2.BidRequest{Imp: []openrtb2.Imp{
						{ID: "imp-id3", Ext: json.RawMessage(`{"bidder":{"placementId":"1234"}}`)},
					}},
					BidderName:            "bidderC",
					BidderStoredResponses: nil,
				},
			},
		},
		{
			description: "Request with 2 imps: with 1 bidders stored bid response and imp without stored responses and with the same bidder",
			storedBidResponses: map[string]map[string]json.RawMessage{
				"imp-id2": {"bidderA": bidRespId2},
			},
			imps: []openrtb2.Imp{
				{
					ID:  "imp-id1",
					Ext: json.RawMessage(`{"prebid":{"bidder":{"bidderA":{"placementId":"123"}}}}`),
				},
				{
					ID:  "imp-id2",
					Ext: json.RawMessage(`{"prebid":{"bidder":{"bidderA":{"placementId":"123"}}}}`),
				},
			},
			expectedBidderRequests: map[string]BidderRequest{
				"bidderA": {
					BidRequest: &openrtb2.BidRequest{Imp: []openrtb2.Imp{
						{ID: "imp-id1", Ext: json.RawMessage(`{"bidder":{"placementId":"123"}}`)},
					}},
					BidderName: "bidderA",
					BidderStoredResponses: map[string]json.RawMessage{
						"imp-id2": bidRespId2},
				},
			},
		},
		{
			description: "Request with 2 imps with stored responses and with the same bidder",
			storedBidResponses: map[string]map[string]json.RawMessage{
				"imp-id1": {"bidderA": bidRespId1},
				"imp-id2": {"bidderA": bidRespId2},
			},
			imps: []openrtb2.Imp{
				{
					ID:  "imp-id1",
					Ext: json.RawMessage(`"prebid": {}`),
				},
				{
					ID:  "imp-id2",
					Ext: json.RawMessage(`"prebid": {}`),
				},
			},
			expectedBidderRequests: map[string]BidderRequest{
				"bidderA": {
					BidRequest: &openrtb2.BidRequest{Imp: nil},
					BidderName: "bidderA",
					BidderStoredResponses: map[string]json.RawMessage{
						"imp-id1": bidRespId1,
						"imp-id2": bidRespId2,
					},
				},
			},
		},
	}

	for _, test := range testCases {

		gdprPermissionsBuilder := fakePermissionsBuilder{
			permissions: &permissionsMock{
				allowAllBidders: true,
			},
		}.Builder
		tcf2ConfigBuilder := fakeTCF2ConfigBuilder{
			cfg: gdpr.NewTCF2Config(config.TCF2{}, config.AccountGDPR{}),
		}.Builder

		auctionReq := AuctionRequest{
			BidRequestWrapper:  &openrtb_ext.RequestWrapper{BidRequest: &openrtb2.BidRequest{Imp: test.imps}},
			UserSyncs:          &emptyUsersync{},
			StoredBidResponses: test.storedBidResponses,
		}

		reqSplitter := &requestSplitter{
			bidderToSyncerKey: map[string]string{},
			me:                &metrics.MetricsEngineMock{},
			privacyConfig:     config.Privacy{},
			gdprPermsBuilder:  gdprPermissionsBuilder,
			tcf2ConfigBuilder: tcf2ConfigBuilder,
			hostSChainNode:    nil,
			bidderInfo:        config.BidderInfos{},
		}

		actualBidderRequests, _, err := reqSplitter.cleanOpenRTBRequests(context.Background(), auctionReq, nil, gdpr.SignalNo)
		assert.Empty(t, err, "No errors should be returned")
		assert.Len(t, actualBidderRequests, len(test.expectedBidderRequests), "result len doesn't match for testCase %s", test.description)
		for _, actualBidderRequest := range actualBidderRequests {
			bidderName := string(actualBidderRequest.BidderName)
			assert.Equal(t, test.expectedBidderRequests[bidderName].BidRequest.Imp, actualBidderRequest.BidRequest.Imp, "incorrect Impressions for testCase %s", test.description)
			assert.Equal(t, test.expectedBidderRequests[bidderName].BidderStoredResponses, actualBidderRequest.BidderStoredResponses, "incorrect Bidder Stored Responses for testCase %s", test.description)
		}
	}
}

func TestCleanOpenRTBRequestsCCPA(t *testing.T) {
	trueValue, falseValue := true, false

	testCases := []struct {
		description         string
		reqExt              json.RawMessage
		ccpaConsent         string
		ccpaHostEnabled     bool
		ccpaAccountEnabled  *bool
		expectDataScrub     bool
		expectPrivacyLabels metrics.PrivacyLabels
	}{
		{
			description:        "Feature Flags Enabled - Opt Out",
			ccpaConsent:        "1-Y-",
			ccpaHostEnabled:    true,
			ccpaAccountEnabled: &trueValue,
			expectDataScrub:    true,
			expectPrivacyLabels: metrics.PrivacyLabels{
				CCPAProvided: true,
				CCPAEnforced: true,
			},
		},
		{
			description:        "Feature Flags Enabled - Opt In",
			ccpaConsent:        "1-N-",
			ccpaHostEnabled:    true,
			ccpaAccountEnabled: &trueValue,
			expectDataScrub:    false,
			expectPrivacyLabels: metrics.PrivacyLabels{
				CCPAProvided: true,
				CCPAEnforced: false,
			},
		},
		{
			description:        "Feature Flags Enabled - No Sale Star - Doesn't Scrub",
			reqExt:             json.RawMessage(`{"prebid":{"nosale":["*"]}}`),
			ccpaConsent:        "1-Y-",
			ccpaHostEnabled:    true,
			ccpaAccountEnabled: &trueValue,
			expectDataScrub:    false,
			expectPrivacyLabels: metrics.PrivacyLabels{
				CCPAProvided: true,
				CCPAEnforced: false,
			},
		},
		{
			description:        "Feature Flags Enabled - No Sale Specific Bidder - Doesn't Scrub",
			reqExt:             json.RawMessage(`{"prebid":{"nosale":["appnexus"]}}`),
			ccpaConsent:        "1-Y-",
			ccpaHostEnabled:    true,
			ccpaAccountEnabled: &trueValue,
			expectDataScrub:    false,
			expectPrivacyLabels: metrics.PrivacyLabels{
				CCPAProvided: true,
				CCPAEnforced: true,
			},
		},
		{
			description:        "Feature Flags Enabled - No Sale Different Bidder - Scrubs",
			reqExt:             json.RawMessage(`{"prebid":{"nosale":["rubicon"]}}`),
			ccpaConsent:        "1-Y-",
			ccpaHostEnabled:    true,
			ccpaAccountEnabled: &trueValue,
			expectDataScrub:    true,
			expectPrivacyLabels: metrics.PrivacyLabels{
				CCPAProvided: true,
				CCPAEnforced: true,
			},
		},
		{
			description:        "Feature flags Account CCPA enabled, host CCPA disregarded - Opt Out",
			ccpaConsent:        "1-Y-",
			ccpaHostEnabled:    false,
			ccpaAccountEnabled: &trueValue,
			expectDataScrub:    true,
			expectPrivacyLabels: metrics.PrivacyLabels{
				CCPAProvided: true,
				CCPAEnforced: true,
			},
		},
		{
			description:        "Feature flags Account CCPA disabled, host CCPA disregarded",
			ccpaConsent:        "1-Y-",
			ccpaHostEnabled:    true,
			ccpaAccountEnabled: &falseValue,
			expectDataScrub:    false,
			expectPrivacyLabels: metrics.PrivacyLabels{
				CCPAProvided: true,
				CCPAEnforced: false,
			},
		},
		{
			description:        "Feature flags Account CCPA not specified, host CCPA enabled - Opt Out",
			ccpaConsent:        "1-Y-",
			ccpaHostEnabled:    true,
			ccpaAccountEnabled: nil,
			expectDataScrub:    true,
			expectPrivacyLabels: metrics.PrivacyLabels{
				CCPAProvided: true,
				CCPAEnforced: true,
			},
		},
		{
			description:        "Feature flags Account CCPA not specified, host CCPA disabled",
			ccpaConsent:        "1-Y-",
			ccpaHostEnabled:    false,
			ccpaAccountEnabled: nil,
			expectDataScrub:    false,
			expectPrivacyLabels: metrics.PrivacyLabels{
				CCPAProvided: true,
				CCPAEnforced: false,
			},
		},
	}

	for _, test := range testCases {
		req := newBidRequest(t)
		req.Ext = test.reqExt
		req.Regs = &openrtb2.Regs{
			Ext: json.RawMessage(`{"us_privacy":"` + test.ccpaConsent + `"}`),
		}

		privacyConfig := config.Privacy{
			CCPA: config.CCPA{
				Enforce: test.ccpaHostEnabled,
			},
		}

		accountConfig := config.Account{
			CCPA: config.AccountCCPA{
				Enabled: test.ccpaAccountEnabled,
			},
		}

		auctionReq := AuctionRequest{
			BidRequestWrapper: &openrtb_ext.RequestWrapper{BidRequest: req},
			UserSyncs:         &emptyUsersync{},
			Account:           accountConfig,
		}

		gdprPermissionsBuilder := fakePermissionsBuilder{
			permissions: &permissionsMock{
				allowAllBidders: true,
			},
		}.Builder
		tcf2ConfigBuilder := fakeTCF2ConfigBuilder{
			cfg: gdpr.NewTCF2Config(config.TCF2{}, accountConfig.GDPR),
		}.Builder

		bidderToSyncerKey := map[string]string{}
		reqSplitter := &requestSplitter{
			bidderToSyncerKey: bidderToSyncerKey,
			me:                &metrics.MetricsEngineMock{},
			privacyConfig:     privacyConfig,
			gdprPermsBuilder:  gdprPermissionsBuilder,
			tcf2ConfigBuilder: tcf2ConfigBuilder,
			hostSChainNode:    nil,
			bidderInfo:        config.BidderInfos{},
		}

		bidderRequests, privacyLabels, errs := reqSplitter.cleanOpenRTBRequests(context.Background(), auctionReq, nil, gdpr.SignalNo)
		result := bidderRequests[0]

		assert.Nil(t, errs)
		if test.expectDataScrub {
			assert.Equal(t, result.BidRequest.User.BuyerUID, "", test.description+":User.BuyerUID")
			assert.Equal(t, result.BidRequest.Device.DIDMD5, "", test.description+":Device.DIDMD5")
		} else {
			assert.NotEqual(t, result.BidRequest.User.BuyerUID, "", test.description+":User.BuyerUID")
			assert.NotEqual(t, result.BidRequest.Device.DIDMD5, "", test.description+":Device.DIDMD5")
		}
		assert.Equal(t, test.expectPrivacyLabels, privacyLabels, test.description+":PrivacyLabels")
	}
}

func TestCleanOpenRTBRequestsCCPAErrors(t *testing.T) {
	testCases := []struct {
		description string
		reqExt      json.RawMessage
		reqRegsExt  json.RawMessage
		expectError error
	}{
		{
			description: "Invalid Consent",
			reqExt:      json.RawMessage(`{"prebid":{"nosale":["*"]}}`),
			reqRegsExt:  json.RawMessage(`{"us_privacy":"malformed"}`),
			expectError: &errortypes.Warning{
				Message:     "request.regs.ext.us_privacy must contain 4 characters",
				WarningCode: errortypes.InvalidPrivacyConsentWarningCode,
			},
		},
		{
			description: "Invalid No Sale Bidders",
			reqExt:      json.RawMessage(`{"prebid":{"nosale":["*", "another"]}}`),
			reqRegsExt:  json.RawMessage(`{"us_privacy":"1NYN"}`),
			expectError: errors.New("request.ext.prebid.nosale is invalid: can only specify all bidders if no other bidders are provided"),
		},
	}

	for _, test := range testCases {
		req := newBidRequest(t)
		req.Ext = test.reqExt
		req.Regs = &openrtb2.Regs{Ext: test.reqRegsExt}

		var reqExtStruct openrtb_ext.ExtRequest
		err := json.Unmarshal(req.Ext, &reqExtStruct)
		assert.NoError(t, err, test.description+":marshal_ext")

		auctionReq := AuctionRequest{
			BidRequestWrapper: &openrtb_ext.RequestWrapper{BidRequest: req},
			UserSyncs:         &emptyUsersync{},
		}

		gdprPermissionsBuilder := fakePermissionsBuilder{
			permissions: &permissionsMock{
				allowAllBidders: true,
			},
		}.Builder
		tcf2ConfigBuilder := fakeTCF2ConfigBuilder{
			cfg: gdpr.NewTCF2Config(config.TCF2{}, config.AccountGDPR{}),
		}.Builder

		privacyConfig := config.Privacy{
			CCPA: config.CCPA{
				Enforce: true,
			},
		}
		bidderToSyncerKey := map[string]string{}
		metrics := metrics.MetricsEngineMock{}

		reqSplitter := &requestSplitter{
			bidderToSyncerKey: bidderToSyncerKey,
			me:                &metrics,
			privacyConfig:     privacyConfig,
			gdprPermsBuilder:  gdprPermissionsBuilder,
			tcf2ConfigBuilder: tcf2ConfigBuilder,
			hostSChainNode:    nil,
			bidderInfo:        config.BidderInfos{},
		}

		_, _, errs := reqSplitter.cleanOpenRTBRequests(context.Background(), auctionReq, &reqExtStruct, gdpr.SignalNo)

		assert.ElementsMatch(t, []error{test.expectError}, errs, test.description)
	}
}

func TestCleanOpenRTBRequestsCOPPA(t *testing.T) {
	testCases := []struct {
		description         string
		coppa               int8
		expectDataScrub     bool
		expectPrivacyLabels metrics.PrivacyLabels
	}{
		{
			description:     "Enabled",
			coppa:           1,
			expectDataScrub: true,
			expectPrivacyLabels: metrics.PrivacyLabels{
				COPPAEnforced: true,
			},
		},
		{
			description:     "Disabled",
			coppa:           0,
			expectDataScrub: false,
			expectPrivacyLabels: metrics.PrivacyLabels{
				COPPAEnforced: false,
			},
		},
	}

	for _, test := range testCases {
		req := newBidRequest(t)
		req.Regs = &openrtb2.Regs{COPPA: test.coppa}

		auctionReq := AuctionRequest{
			BidRequestWrapper: &openrtb_ext.RequestWrapper{BidRequest: req},
			UserSyncs:         &emptyUsersync{},
		}

		gdprPermissionsBuilder := fakePermissionsBuilder{
			permissions: &permissionsMock{
				allowAllBidders: true,
			},
		}.Builder
		tcf2ConfigBuilder := fakeTCF2ConfigBuilder{
			cfg: gdpr.NewTCF2Config(config.TCF2{}, config.AccountGDPR{}),
		}.Builder

		bidderToSyncerKey := map[string]string{}
		metrics := metrics.MetricsEngineMock{}

		reqSplitter := &requestSplitter{
			bidderToSyncerKey: bidderToSyncerKey,
			me:                &metrics,
			privacyConfig:     config.Privacy{},
			gdprPermsBuilder:  gdprPermissionsBuilder,
			tcf2ConfigBuilder: tcf2ConfigBuilder,
			hostSChainNode:    nil,
			bidderInfo:        config.BidderInfos{},
		}

		bidderRequests, privacyLabels, errs := reqSplitter.cleanOpenRTBRequests(context.Background(), auctionReq, nil, gdpr.SignalNo)
		result := bidderRequests[0]

		assert.Nil(t, errs)
		if test.expectDataScrub {
			assert.Equal(t, result.BidRequest.User.BuyerUID, "", test.description+":User.BuyerUID")
			assert.Equal(t, result.BidRequest.User.Yob, int64(0), test.description+":User.Yob")
		} else {
			assert.NotEqual(t, result.BidRequest.User.BuyerUID, "", test.description+":User.BuyerUID")
			assert.NotEqual(t, result.BidRequest.User.Yob, int64(0), test.description+":User.Yob")
		}
		assert.Equal(t, test.expectPrivacyLabels, privacyLabels, test.description+":PrivacyLabels")
	}
}

func TestCleanOpenRTBRequestsSChain(t *testing.T) {
	const seller1SChain string = `"schain":{"complete":1,"nodes":[{"asi":"directseller1.com","sid":"00001","rid":"BidRequest1","hp":1}],"ver":"1.0"}`
	const seller2SChain string = `"schain":{"complete":2,"nodes":[{"asi":"directseller2.com","sid":"00002","rid":"BidRequest2","hp":2}],"ver":"2.0"}`

	testCases := []struct {
		description   string
		inExt         json.RawMessage
		inSourceExt   json.RawMessage
		outRequestExt json.RawMessage
		outSourceExt  json.RawMessage
		hasError      bool
	}{
		{
			description:   "source.ext is nil",
			inExt:         json.RawMessage{},
			inSourceExt:   nil,
			outRequestExt: json.RawMessage{},
			outSourceExt:  nil,
		},
		{
			description:   "source.ext is defined with length 0",
			inExt:         json.RawMessage{},
			inSourceExt:   json.RawMessage{},
			outRequestExt: json.RawMessage{},
			outSourceExt:  json.RawMessage{},
		},
		{
			description:   "ORTB 2.5 chain at source.ext.schain",
			inExt:         json.RawMessage{},
			inSourceExt:   json.RawMessage(`{` + seller1SChain + `}`),
			outRequestExt: json.RawMessage{},
			outSourceExt:  json.RawMessage(`{` + seller1SChain + `}`),
		},
		{
			description:   "ORTB 2.5 schain at request.ext.prebid.schains",
			inExt:         json.RawMessage(`{"prebid":{"schains":[{"bidders":["appnexus"],` + seller1SChain + `}]}}`),
			inSourceExt:   json.RawMessage{},
			outRequestExt: json.RawMessage(`{"prebid":{}}`),
			outSourceExt:  json.RawMessage(`{` + seller1SChain + `}`),
		},
		{
			description:   "schainwriter instantation error -- multiple bidder schains in ext.prebid.schains.",
			inExt:         json.RawMessage(`{"prebid":{"schains":[{"bidders":["appnexus"],` + seller1SChain + `},{"bidders":["appnexus"],` + seller2SChain + `}]}}`),
			inSourceExt:   json.RawMessage(`{` + seller1SChain + `}`),
			outRequestExt: nil,
			outSourceExt:  nil,
			hasError:      true,
		},
	}

	for _, test := range testCases {
		req := newBidRequest(t)
		if test.inSourceExt != nil {
			req.Source.Ext = test.inSourceExt
		}

		var extRequest *openrtb_ext.ExtRequest
		if test.inExt != nil {
			req.Ext = test.inExt
			unmarshaledExt, err := extractBidRequestExt(req)
			assert.NoErrorf(t, err, test.description+":Error unmarshaling inExt")
			extRequest = unmarshaledExt
		}

		auctionReq := AuctionRequest{
			BidRequestWrapper: &openrtb_ext.RequestWrapper{BidRequest: req},
			UserSyncs:         &emptyUsersync{},
		}

		gdprPermissionsBuilder := fakePermissionsBuilder{
			permissions: &permissionsMock{
				allowAllBidders: true,
			},
		}.Builder
		tcf2ConfigBuilder := fakeTCF2ConfigBuilder{
			cfg: gdpr.NewTCF2Config(config.TCF2{}, config.AccountGDPR{}),
		}.Builder

		reqSplitter := &requestSplitter{
			bidderToSyncerKey: map[string]string{},
			me:                &metrics.MetricsEngineMock{},
			privacyConfig:     config.Privacy{},
			gdprPermsBuilder:  gdprPermissionsBuilder,
			tcf2ConfigBuilder: tcf2ConfigBuilder,
			hostSChainNode:    nil,
			bidderInfo:        config.BidderInfos{},
		}

		bidderRequests, _, errs := reqSplitter.cleanOpenRTBRequests(context.Background(), auctionReq, extRequest, gdpr.SignalNo)
		if test.hasError == true {
			assert.NotNil(t, errs)
			assert.Len(t, bidderRequests, 0)
		} else {
			result := bidderRequests[0]
			assert.Nil(t, errs)
			assert.Equal(t, test.outSourceExt, result.BidRequest.Source.Ext, test.description+":Source.Ext")
			assert.Equal(t, test.outRequestExt, result.BidRequest.Ext, test.description+":Ext")
		}
	}
}

func TestCleanOpenRTBRequestsBidderParams(t *testing.T) {
	testCases := []struct {
		description string
		inExt       json.RawMessage
		expectedExt map[string]json.RawMessage
		hasError    bool
	}{
		{
			description: "Nil Bidder params",
			inExt:       nil,
			expectedExt: getExpectedReqExt(true, false, false),
			hasError:    false,
		},
		{
			description: "Bidder params for single partner",
			inExt:       json.RawMessage(`{"prebid":{"bidderparams": {"pubmatic": {"profile":1234,"version":2}}}}`),
			expectedExt: getExpectedReqExt(false, true, false),
			hasError:    false,
		},
		{
			description: "Bidder params for two partners",
			inExt:       json.RawMessage(`{"prebid":{"bidderparams": {"pubmatic": {"profile":1234,"version":2}, "appnexus": {"key1": 123, "key2": {"innerKey1":"innerValue1"}} }}}`),
			expectedExt: getExpectedReqExt(false, true, true),
			hasError:    false,
		},
	}

	for _, test := range testCases {
		req := newBidRequestWithBidderParams(t)
		var extRequest *openrtb_ext.ExtRequest
		if test.inExt != nil {
			req.Ext = test.inExt
			unmarshaledExt, err := extractBidRequestExt(req)
			assert.NoErrorf(t, err, test.description+":Error unmarshaling inExt")
			extRequest = unmarshaledExt
		}

		auctionReq := AuctionRequest{
			BidRequestWrapper: &openrtb_ext.RequestWrapper{BidRequest: req},
			UserSyncs:         &emptyUsersync{},
		}

		gdprPermissionsBuilder := fakePermissionsBuilder{
			permissions: &permissionsMock{
				allowAllBidders: true,
			},
		}.Builder
		tcf2ConfigBuilder := fakeTCF2ConfigBuilder{
			cfg: gdpr.NewTCF2Config(config.TCF2{}, config.AccountGDPR{}),
		}.Builder

		reqSplitter := &requestSplitter{
			bidderToSyncerKey: map[string]string{},
			me:                &metrics.MetricsEngineMock{},
			privacyConfig:     config.Privacy{},
			gdprPermsBuilder:  gdprPermissionsBuilder,
			tcf2ConfigBuilder: tcf2ConfigBuilder,
			hostSChainNode:    nil,
			bidderInfo:        config.BidderInfos{},
		}

		bidderRequests, _, errs := reqSplitter.cleanOpenRTBRequests(context.Background(), auctionReq, extRequest, gdpr.SignalNo)
		if test.hasError == true {
			assert.NotNil(t, errs)
			assert.Len(t, bidderRequests, 0)
		} else {
			assert.Nil(t, errs)
			for _, r := range bidderRequests {
				expected := test.expectedExt[r.BidderName.String()]
				actual := r.BidRequest.Ext
				assert.Equal(t, expected, actual, test.description+" Req:Ext.Prebid.BidderParams")
			}
		}
	}
}

func getExpectedReqExt(nilExt, includePubmaticParams, includeAppnexusParams bool) map[string]json.RawMessage {
	bidderParamsMap := make(map[string]json.RawMessage)

	if nilExt {
		bidderParamsMap["pubmatic"] = json.RawMessage(``)
		bidderParamsMap["appnexus"] = json.RawMessage(``)
		return bidderParamsMap
	}

	if includePubmaticParams {
		bidderParamsMap["pubmatic"] = json.RawMessage(`{"prebid":{"bidderparams":{"profile":1234,"version":2}}}`)
	} else {
		bidderParamsMap["pubmatic"] = json.RawMessage(`{"prebid":{}}`)
	}

	if includeAppnexusParams {
		bidderParamsMap["appnexus"] = json.RawMessage(`{"prebid":{"bidderparams":{"key1":123,"key2":{"innerKey1":"innerValue1"}}}}`)
	} else {
		bidderParamsMap["appnexus"] = json.RawMessage(`{"prebid":{}}`)
	}

	return bidderParamsMap
}

func TestExtractBidRequestExt(t *testing.T) {
	var boolFalse, boolTrue *bool = new(bool), new(bool)
	*boolFalse = false
	*boolTrue = true

	testCases := []struct {
		desc          string
		inBidRequest  *openrtb2.BidRequest
		outRequestExt *openrtb_ext.ExtRequest
		outError      error
	}{
		{
			desc:         "Valid vastxml.returnCreative set to false",
			inBidRequest: &openrtb2.BidRequest{Ext: json.RawMessage(`{"prebid":{"debug":true,"cache":{"vastxml":{"returnCreative":false}}}}`)},
			outRequestExt: &openrtb_ext.ExtRequest{
				Prebid: openrtb_ext.ExtRequestPrebid{
					Debug: true,
					Cache: &openrtb_ext.ExtRequestPrebidCache{
						VastXML: &openrtb_ext.ExtRequestPrebidCacheVAST{
							ReturnCreative: boolFalse,
						},
					},
				},
			},
			outError: nil,
		},
		{
			desc:         "Valid vastxml.returnCreative set to true",
			inBidRequest: &openrtb2.BidRequest{Ext: json.RawMessage(`{"prebid":{"debug":true,"cache":{"vastxml":{"returnCreative":true}}}}`)},
			outRequestExt: &openrtb_ext.ExtRequest{
				Prebid: openrtb_ext.ExtRequestPrebid{
					Debug: true,
					Cache: &openrtb_ext.ExtRequestPrebidCache{
						VastXML: &openrtb_ext.ExtRequestPrebidCacheVAST{
							ReturnCreative: boolTrue,
						},
					},
				},
			},
			outError: nil,
		},
		{
			desc:          "bidRequest nil, we expect an error",
			inBidRequest:  nil,
			outRequestExt: &openrtb_ext.ExtRequest{},
			outError:      fmt.Errorf("Error bidRequest should not be nil"),
		},
		{
			desc:          "Non-nil bidRequest with empty Ext, we expect a blank requestExt",
			inBidRequest:  &openrtb2.BidRequest{},
			outRequestExt: &openrtb_ext.ExtRequest{},
			outError:      nil,
		},
		{
			desc:          "Non-nil bidRequest with non-empty, invalid Ext, we expect unmarshaling error",
			inBidRequest:  &openrtb2.BidRequest{Ext: json.RawMessage(`invalid`)},
			outRequestExt: &openrtb_ext.ExtRequest{},
			outError:      fmt.Errorf("Error decoding Request.ext : invalid character 'i' looking for beginning of value"),
		},
	}
	for _, test := range testCases {
		actualRequestExt, actualErr := extractBidRequestExt(test.inBidRequest)

		// Given that assert.Equal asserts pointer variable equality based on the equality of the referenced values (as opposed to
		// the memory addresses) the call below asserts whether or not *test.outRequestExt.Prebid.Cache.VastXML.ReturnCreative boolean
		// value is equal to that of *actualRequestExt.Prebid.Cache.VastXML.ReturnCreative
		assert.Equal(t, test.outRequestExt, actualRequestExt, "%s. Unexpected RequestExt value. \n", test.desc)
		assert.Equal(t, test.outError, actualErr, "%s. Unexpected error value. \n", test.desc)
	}
}

func TestGetExtCacheInstructions(t *testing.T) {
	var boolFalse, boolTrue *bool = new(bool), new(bool)
	*boolFalse = false
	*boolTrue = true

	testCases := []struct {
		desc                 string
		inRequestExt         *openrtb_ext.ExtRequest
		outCacheInstructions extCacheInstructions
	}{
		{
			desc:         "Nil inRequestExt, all cache flags false except for returnCreative that defaults to true",
			inRequestExt: nil,
			outCacheInstructions: extCacheInstructions{
				cacheBids:      false,
				cacheVAST:      false,
				returnCreative: true,
			},
		},
		{
			desc:         "Non-nil inRequestExt, nil Cache field, all cache flags false except for returnCreative that defaults to true",
			inRequestExt: &openrtb_ext.ExtRequest{Prebid: openrtb_ext.ExtRequestPrebid{Cache: nil}},
			outCacheInstructions: extCacheInstructions{
				cacheBids:      false,
				cacheVAST:      false,
				returnCreative: true,
			},
		},
		{
			desc: "Non-nil Cache field, both ExtRequestPrebidCacheBids and ExtRequestPrebidCacheVAST nil returnCreative that defaults to true",
			inRequestExt: &openrtb_ext.ExtRequest{
				Prebid: openrtb_ext.ExtRequestPrebid{
					Cache: &openrtb_ext.ExtRequestPrebidCache{
						Bids:    nil,
						VastXML: nil,
					},
				},
			},
			outCacheInstructions: extCacheInstructions{
				cacheBids:      false,
				cacheVAST:      false,
				returnCreative: true,
			},
		},
		{
			desc: "Non-nil ExtRequest.Cache.ExtRequestPrebidCacheVAST with unspecified ReturnCreative field, cacheVAST = true and returnCreative defaults to true",
			inRequestExt: &openrtb_ext.ExtRequest{
				Prebid: openrtb_ext.ExtRequestPrebid{
					Cache: &openrtb_ext.ExtRequestPrebidCache{
						Bids:    nil,
						VastXML: &openrtb_ext.ExtRequestPrebidCacheVAST{},
					},
				},
			},
			outCacheInstructions: extCacheInstructions{
				cacheBids:      false,
				cacheVAST:      true,
				returnCreative: true, // default value
			},
		},
		{
			desc: "Non-nil ExtRequest.Cache.ExtRequestPrebidCacheVAST where ReturnCreative is set to false, cacheVAST = true and returnCreative = false",
			inRequestExt: &openrtb_ext.ExtRequest{
				Prebid: openrtb_ext.ExtRequestPrebid{
					Cache: &openrtb_ext.ExtRequestPrebidCache{
						Bids:    nil,
						VastXML: &openrtb_ext.ExtRequestPrebidCacheVAST{ReturnCreative: boolFalse},
					},
				},
			},
			outCacheInstructions: extCacheInstructions{
				cacheBids:      false,
				cacheVAST:      true,
				returnCreative: false,
			},
		},
		{
			desc: "Non-nil ExtRequest.Cache.ExtRequestPrebidCacheVAST where ReturnCreative is set to true, cacheVAST = true and returnCreative = true",
			inRequestExt: &openrtb_ext.ExtRequest{
				Prebid: openrtb_ext.ExtRequestPrebid{
					Cache: &openrtb_ext.ExtRequestPrebidCache{
						Bids:    nil,
						VastXML: &openrtb_ext.ExtRequestPrebidCacheVAST{ReturnCreative: boolTrue},
					},
				},
			},
			outCacheInstructions: extCacheInstructions{
				cacheBids:      false,
				cacheVAST:      true,
				returnCreative: true,
			},
		},
		{
			desc: "Non-nil ExtRequest.Cache.ExtRequestPrebidCacheBids with unspecified ReturnCreative field, cacheBids = true and returnCreative defaults to true",
			inRequestExt: &openrtb_ext.ExtRequest{
				Prebid: openrtb_ext.ExtRequestPrebid{
					Cache: &openrtb_ext.ExtRequestPrebidCache{
						Bids:    &openrtb_ext.ExtRequestPrebidCacheBids{},
						VastXML: nil,
					},
				},
			},
			outCacheInstructions: extCacheInstructions{
				cacheBids:      true,
				cacheVAST:      false,
				returnCreative: true, // default value
			},
		},
		{
			desc: "Non-nil ExtRequest.Cache.ExtRequestPrebidCacheBids where ReturnCreative is set to false, cacheBids = true and returnCreative  = false",
			inRequestExt: &openrtb_ext.ExtRequest{
				Prebid: openrtb_ext.ExtRequestPrebid{
					Cache: &openrtb_ext.ExtRequestPrebidCache{
						Bids:    &openrtb_ext.ExtRequestPrebidCacheBids{ReturnCreative: boolFalse},
						VastXML: nil,
					},
				},
			},
			outCacheInstructions: extCacheInstructions{
				cacheBids:      true,
				cacheVAST:      false,
				returnCreative: false,
			},
		},
		{
			desc: "Non-nil ExtRequest.Cache.ExtRequestPrebidCacheBids where ReturnCreative is set to true, cacheBids = true and returnCreative  = true",
			inRequestExt: &openrtb_ext.ExtRequest{
				Prebid: openrtb_ext.ExtRequestPrebid{
					Cache: &openrtb_ext.ExtRequestPrebidCache{
						Bids:    &openrtb_ext.ExtRequestPrebidCacheBids{ReturnCreative: boolTrue},
						VastXML: nil,
					},
				},
			},
			outCacheInstructions: extCacheInstructions{
				cacheBids:      true,
				cacheVAST:      false,
				returnCreative: true,
			},
		},
		{
			desc: "Non-nil ExtRequest.Cache.ExtRequestPrebidCacheBids and ExtRequest.Cache.ExtRequestPrebidCacheVAST, neither specify a ReturnCreative field value, all extCacheInstructions fields set to true",
			inRequestExt: &openrtb_ext.ExtRequest{
				Prebid: openrtb_ext.ExtRequestPrebid{
					Cache: &openrtb_ext.ExtRequestPrebidCache{
						Bids:    &openrtb_ext.ExtRequestPrebidCacheBids{},
						VastXML: &openrtb_ext.ExtRequestPrebidCacheVAST{},
					},
				},
			},
			outCacheInstructions: extCacheInstructions{
				cacheBids:      true,
				cacheVAST:      true,
				returnCreative: true,
			},
		},
		{
			desc: "Non-nil ExtRequest.Cache.ExtRequestPrebidCacheBids and ExtRequest.Cache.ExtRequestPrebidCacheVAST sets ReturnCreative to true, all extCacheInstructions fields set to true",
			inRequestExt: &openrtb_ext.ExtRequest{
				Prebid: openrtb_ext.ExtRequestPrebid{
					Cache: &openrtb_ext.ExtRequestPrebidCache{
						Bids:    &openrtb_ext.ExtRequestPrebidCacheBids{},
						VastXML: &openrtb_ext.ExtRequestPrebidCacheVAST{ReturnCreative: boolTrue},
					},
				},
			},
			outCacheInstructions: extCacheInstructions{
				cacheBids:      true,
				cacheVAST:      true,
				returnCreative: true,
			},
		},
		{
			desc: "Non-nil ExtRequest.Cache.ExtRequestPrebidCacheBids and ExtRequest.Cache.ExtRequestPrebidCacheVAST sets ReturnCreative to false, returnCreative = false",
			inRequestExt: &openrtb_ext.ExtRequest{
				Prebid: openrtb_ext.ExtRequestPrebid{
					Cache: &openrtb_ext.ExtRequestPrebidCache{
						Bids:    &openrtb_ext.ExtRequestPrebidCacheBids{},
						VastXML: &openrtb_ext.ExtRequestPrebidCacheVAST{ReturnCreative: boolFalse},
					},
				},
			},
			outCacheInstructions: extCacheInstructions{
				cacheBids:      true,
				cacheVAST:      true,
				returnCreative: false,
			},
		},
		{
			desc: "Non-nil ExtRequest.Cache.ExtRequestPrebidCacheVAST and ExtRequest.Cache.ExtRequestPrebidCacheBids sets ReturnCreative to true, all extCacheInstructions fields set to true",
			inRequestExt: &openrtb_ext.ExtRequest{
				Prebid: openrtb_ext.ExtRequestPrebid{
					Cache: &openrtb_ext.ExtRequestPrebidCache{
						Bids:    &openrtb_ext.ExtRequestPrebidCacheBids{ReturnCreative: boolTrue},
						VastXML: &openrtb_ext.ExtRequestPrebidCacheVAST{},
					},
				},
			},
			outCacheInstructions: extCacheInstructions{
				cacheBids:      true,
				cacheVAST:      true,
				returnCreative: true,
			},
		},
		{
			desc: "Non-nil ExtRequest.Cache.ExtRequestPrebidCacheVAST and ExtRequest.Cache.ExtRequestPrebidCacheBids sets ReturnCreative to false, returnCreative = false",
			inRequestExt: &openrtb_ext.ExtRequest{
				Prebid: openrtb_ext.ExtRequestPrebid{
					Cache: &openrtb_ext.ExtRequestPrebidCache{
						Bids:    &openrtb_ext.ExtRequestPrebidCacheBids{ReturnCreative: boolFalse},
						VastXML: &openrtb_ext.ExtRequestPrebidCacheVAST{},
					},
				},
			},
			outCacheInstructions: extCacheInstructions{
				cacheBids:      true,
				cacheVAST:      true,
				returnCreative: false,
			},
		},
		{
			desc: "Non-nil ExtRequest.Cache.ExtRequestPrebidCacheVAST and ExtRequest.Cache.ExtRequestPrebidCacheBids set different ReturnCreative values, returnCreative = true because one of them is true",
			inRequestExt: &openrtb_ext.ExtRequest{
				Prebid: openrtb_ext.ExtRequestPrebid{
					Cache: &openrtb_ext.ExtRequestPrebidCache{
						Bids:    &openrtb_ext.ExtRequestPrebidCacheBids{ReturnCreative: boolFalse},
						VastXML: &openrtb_ext.ExtRequestPrebidCacheVAST{ReturnCreative: boolTrue},
					},
				},
			},
			outCacheInstructions: extCacheInstructions{
				cacheBids:      true,
				cacheVAST:      true,
				returnCreative: true,
			},
		},
		{
			desc: "Non-nil ExtRequest.Cache.ExtRequestPrebidCacheVAST and ExtRequest.Cache.ExtRequestPrebidCacheBids set different ReturnCreative values, returnCreative = true because one of them is true",
			inRequestExt: &openrtb_ext.ExtRequest{
				Prebid: openrtb_ext.ExtRequestPrebid{
					Cache: &openrtb_ext.ExtRequestPrebidCache{
						Bids:    &openrtb_ext.ExtRequestPrebidCacheBids{ReturnCreative: boolTrue},
						VastXML: &openrtb_ext.ExtRequestPrebidCacheVAST{ReturnCreative: boolFalse},
					},
				},
			},
			outCacheInstructions: extCacheInstructions{
				cacheBids:      true,
				cacheVAST:      true,
				returnCreative: true,
			},
		},
	}

	for _, test := range testCases {
		cacheInstructions := getExtCacheInstructions(test.inRequestExt)

		assert.Equal(t, test.outCacheInstructions.cacheBids, cacheInstructions.cacheBids, "%s. Unexpected shouldCacheBids value. \n", test.desc)
		assert.Equal(t, test.outCacheInstructions.cacheVAST, cacheInstructions.cacheVAST, "%s. Unexpected shouldCacheVAST value. \n", test.desc)
		assert.Equal(t, test.outCacheInstructions.returnCreative, cacheInstructions.returnCreative, "%s. Unexpected returnCreative value. \n", test.desc)
	}
}

func TestGetExtTargetData(t *testing.T) {
	type inTest struct {
		requestExt        *openrtb_ext.ExtRequest
		cacheInstructions *extCacheInstructions
	}
	type outTest struct {
		targetData    *targetData
		nilTargetData bool
	}
	testCases := []struct {
		desc string
		in   inTest
		out  outTest
	}{
		{
			"nil requestExt, nil outTargetData",
			inTest{
				requestExt: nil,
				cacheInstructions: &extCacheInstructions{
					cacheBids: true,
					cacheVAST: true,
				},
			},
			outTest{targetData: nil, nilTargetData: true},
		},
		{
			"Valid requestExt, nil Targeting field, nil outTargetData",
			inTest{
				requestExt: &openrtb_ext.ExtRequest{
					Prebid: openrtb_ext.ExtRequestPrebid{
						Targeting: nil,
					},
				},
				cacheInstructions: &extCacheInstructions{
					cacheBids: true,
					cacheVAST: true,
				},
			},
			outTest{targetData: nil, nilTargetData: true},
		},
		{
			"Valid targeting data in requestExt, valid outTargetData",
			inTest{
				requestExt: &openrtb_ext.ExtRequest{
					Prebid: openrtb_ext.ExtRequestPrebid{
						Targeting: &openrtb_ext.ExtRequestTargeting{
							PriceGranularity: openrtb_ext.PriceGranularity{
								Precision: 2,
								Ranges:    []openrtb_ext.GranularityRange{{Min: 0.00, Max: 5.00, Increment: 1.00}},
							},
							IncludeWinners:    true,
							IncludeBidderKeys: true,
						},
					},
				},
				cacheInstructions: &extCacheInstructions{
					cacheBids: true,
					cacheVAST: true,
				},
			},
			outTest{
				targetData: &targetData{
					priceGranularity: openrtb_ext.PriceGranularity{
						Precision: 2,
						Ranges:    []openrtb_ext.GranularityRange{{Min: 0.00, Max: 5.00, Increment: 1.00}},
					},
					includeWinners:    true,
					includeBidderKeys: true,
					includeCacheBids:  true,
					includeCacheVast:  true,
				},
				nilTargetData: false,
			},
		},
	}
	for _, test := range testCases {
		actualTargetData := getExtTargetData(test.in.requestExt, test.in.cacheInstructions)

		if test.out.nilTargetData {
			assert.Nil(t, actualTargetData, "%s. Targeting data should be nil. \n", test.desc)
		} else {
			assert.NotNil(t, actualTargetData, "%s. Targeting data should NOT be nil. \n", test.desc)
			assert.Equal(t, *test.out.targetData, *actualTargetData, "%s. Unexpected targeting data value. \n", test.desc)
		}
	}
}

func TestParseRequestDebugValues(t *testing.T) {
	type inTest struct {
		bidRequest *openrtb2.BidRequest
		requestExt *openrtb_ext.ExtRequest
	}
	testCases := []struct {
		desc string
		in   inTest
		out  bool
	}{
		{
			desc: "Nil bid request, nil requestExt",
			in:   inTest{nil, nil},
			out:  false,
		},
		{
			desc: "bid request test == 0, nil requestExt",
			in:   inTest{&openrtb2.BidRequest{Test: 0}, nil},
			out:  false,
		},
		{
			desc: "Nil bid request, requestExt debug flag false",
			in:   inTest{nil, &openrtb_ext.ExtRequest{Prebid: openrtb_ext.ExtRequestPrebid{Debug: false}}},
			out:  false,
		},
		{
			desc: "bid request test == 0, requestExt debug flag false",
			in:   inTest{&openrtb2.BidRequest{Test: 0}, &openrtb_ext.ExtRequest{Prebid: openrtb_ext.ExtRequestPrebid{Debug: false}}},
			out:  false,
		},
		{
			desc: "bid request test == 1, requestExt debug flag false",
			in:   inTest{&openrtb2.BidRequest{Test: 1}, &openrtb_ext.ExtRequest{Prebid: openrtb_ext.ExtRequestPrebid{Debug: false}}},
			out:  true,
		},
		{
			desc: "bid request test == 0, requestExt debug flag true",
			in:   inTest{&openrtb2.BidRequest{Test: 0}, &openrtb_ext.ExtRequest{Prebid: openrtb_ext.ExtRequestPrebid{Debug: true}}},
			out:  true,
		},
		{
			desc: "bid request test == 1, requestExt debug flag true",
			in:   inTest{&openrtb2.BidRequest{Test: 1}, &openrtb_ext.ExtRequest{Prebid: openrtb_ext.ExtRequestPrebid{Debug: true}}},
			out:  true,
		},
	}
	for _, test := range testCases {
		actualDebugInfo := parseRequestDebugValues(test.in.bidRequest, test.in.requestExt)

		assert.Equal(t, test.out, actualDebugInfo, "%s. Unexpected debug value. \n", test.desc)
	}
}

func TestSetDebugLogValues(t *testing.T) {

	type aTest struct {
		desc               string
		inAccountDebugFlag bool
		inDebugLog         *DebugLog
		expectedDebugLog   *DebugLog
	}

	testGroups := []struct {
		desc      string
		testCases []aTest
	}{

		{
			"nil debug log",
			[]aTest{
				{
					desc:               "accountDebugFlag false, expect all false flags in resulting debugLog",
					inAccountDebugFlag: false,
					inDebugLog:         nil,
					expectedDebugLog:   &DebugLog{},
				},
				{
					desc:               "accountDebugFlag true, expect debugLog.Enabled to be true",
					inAccountDebugFlag: true,
					inDebugLog:         nil,
					expectedDebugLog:   &DebugLog{Enabled: true},
				},
			},
		},
		{
			"non-nil debug log",
			[]aTest{
				{
					desc:               "both accountDebugFlag and DebugEnabledOrOverridden are false, expect debugLog.Enabled to be false",
					inAccountDebugFlag: false,
					inDebugLog:         &DebugLog{},
					expectedDebugLog:   &DebugLog{},
				},
				{
					desc:               "accountDebugFlag false but DebugEnabledOrOverridden is true, expect debugLog.Enabled to be true",
					inAccountDebugFlag: false,
					inDebugLog:         &DebugLog{DebugEnabledOrOverridden: true},
					expectedDebugLog:   &DebugLog{DebugEnabledOrOverridden: true, Enabled: true},
				},
				{
					desc:               "accountDebugFlag true but DebugEnabledOrOverridden is false, expect debugLog.Enabled to be true",
					inAccountDebugFlag: true,
					inDebugLog:         &DebugLog{},
					expectedDebugLog:   &DebugLog{Enabled: true},
				},
				{
					desc:               "Both accountDebugFlag and DebugEnabledOrOverridden are true, expect debugLog.Enabled to be true",
					inAccountDebugFlag: true,
					inDebugLog:         &DebugLog{DebugEnabledOrOverridden: true},
					expectedDebugLog:   &DebugLog{DebugEnabledOrOverridden: true, Enabled: true},
				},
			},
		},
	}

	for _, group := range testGroups {
		for _, tc := range group.testCases {
			// run
			actualDebugLog := setDebugLogValues(tc.inAccountDebugFlag, tc.inDebugLog)
			// assertions
			assert.Equal(t, tc.expectedDebugLog, actualDebugLog, "%s. %s", group.desc, tc.desc)
		}
	}
}

func TestGetExtBidAdjustmentFactors(t *testing.T) {
	testCases := []struct {
		desc                    string
		inRequestExt            *openrtb_ext.ExtRequest
		outBidAdjustmentFactors map[string]float64
	}{
		{
			desc:                    "Nil request ext",
			inRequestExt:            nil,
			outBidAdjustmentFactors: nil,
		},
		{
			desc:                    "Non-nil request ext, nil BidAdjustmentFactors field",
			inRequestExt:            &openrtb_ext.ExtRequest{Prebid: openrtb_ext.ExtRequestPrebid{BidAdjustmentFactors: nil}},
			outBidAdjustmentFactors: nil,
		},
		{
			desc:                    "Non-nil request ext, valid BidAdjustmentFactors field",
			inRequestExt:            &openrtb_ext.ExtRequest{Prebid: openrtb_ext.ExtRequestPrebid{BidAdjustmentFactors: map[string]float64{"bid-factor": 1.0}}},
			outBidAdjustmentFactors: map[string]float64{"bid-factor": 1.0},
		},
	}
	for _, test := range testCases {
		actualBidAdjustmentFactors := getExtBidAdjustmentFactors(test.inRequestExt)

		assert.Equal(t, test.outBidAdjustmentFactors, actualBidAdjustmentFactors, "%s. Unexpected BidAdjustmentFactors value. \n", test.desc)
	}
}

func TestCleanOpenRTBRequestsLMT(t *testing.T) {
	var (
		enabled  int8 = 1
		disabled int8 = 0
	)
	testCases := []struct {
		description         string
		lmt                 *int8
		enforceLMT          bool
		expectDataScrub     bool
		expectPrivacyLabels metrics.PrivacyLabels
	}{
		{
			description:     "Feature Flag Enabled - OpenTRB Enabled",
			lmt:             &enabled,
			enforceLMT:      true,
			expectDataScrub: true,
			expectPrivacyLabels: metrics.PrivacyLabels{
				LMTEnforced: true,
			},
		},
		{
			description:     "Feature Flag Disabled - OpenTRB Enabled",
			lmt:             &enabled,
			enforceLMT:      false,
			expectDataScrub: false,
			expectPrivacyLabels: metrics.PrivacyLabels{
				LMTEnforced: false,
			},
		},
		{
			description:     "Feature Flag Enabled - OpenTRB Disabled",
			lmt:             &disabled,
			enforceLMT:      true,
			expectDataScrub: false,
			expectPrivacyLabels: metrics.PrivacyLabels{
				LMTEnforced: false,
			},
		},
		{
			description:     "Feature Flag Disabled - OpenTRB Disabled",
			lmt:             &disabled,
			enforceLMT:      false,
			expectDataScrub: false,
			expectPrivacyLabels: metrics.PrivacyLabels{
				LMTEnforced: false,
			},
		},
	}

	for _, test := range testCases {
		req := newBidRequest(t)
		req.Device.Lmt = test.lmt

		auctionReq := AuctionRequest{
			BidRequestWrapper: &openrtb_ext.RequestWrapper{BidRequest: req},
			UserSyncs:         &emptyUsersync{},
		}

		gdprPermissionsBuilder := fakePermissionsBuilder{
			permissions: &permissionsMock{
				allowAllBidders: true,
			},
		}.Builder
		tcf2ConfigBuilder := fakeTCF2ConfigBuilder{
			cfg: gdpr.NewTCF2Config(config.TCF2{}, config.AccountGDPR{}),
		}.Builder

		privacyConfig := config.Privacy{
			LMT: config.LMT{
				Enforce: test.enforceLMT,
			},
		}

		reqSplitter := &requestSplitter{
			bidderToSyncerKey: map[string]string{},
			me:                &metrics.MetricsEngineMock{},
			privacyConfig:     privacyConfig,
			gdprPermsBuilder:  gdprPermissionsBuilder,
			tcf2ConfigBuilder: tcf2ConfigBuilder,
			hostSChainNode:    nil,
			bidderInfo:        config.BidderInfos{},
		}

		results, privacyLabels, errs := reqSplitter.cleanOpenRTBRequests(context.Background(), auctionReq, nil, gdpr.SignalNo)
		result := results[0]

		assert.Nil(t, errs)
		if test.expectDataScrub {
			assert.Equal(t, result.BidRequest.User.BuyerUID, "", test.description+":User.BuyerUID")
			assert.Equal(t, result.BidRequest.Device.DIDMD5, "", test.description+":Device.DIDMD5")
		} else {
			assert.NotEqual(t, result.BidRequest.User.BuyerUID, "", test.description+":User.BuyerUID")
			assert.NotEqual(t, result.BidRequest.Device.DIDMD5, "", test.description+":Device.DIDMD5")
		}
		assert.Equal(t, test.expectPrivacyLabels, privacyLabels, test.description+":PrivacyLabels")
	}
}

func TestCleanOpenRTBRequestsGDPR(t *testing.T) {
	tcf2Consent := "COzTVhaOzTVhaGvAAAENAiCIAP_AAH_AAAAAAEEUACCKAAA"
	trueValue, falseValue := true, false

	testCases := []struct {
		description         string
		gdprAccountEnabled  *bool
		gdprHostEnabled     bool
		gdpr                string
		gdprConsent         string
		gdprScrub           bool
		permissionsError    error
		gdprDefaultValue    string
		expectPrivacyLabels metrics.PrivacyLabels
		expectError         bool
	}{
		{
			description:        "Enforce - TCF Invalid",
			gdprAccountEnabled: &trueValue,
			gdprHostEnabled:    true,
			gdpr:               "1",
			gdprConsent:        "malformed",
			gdprScrub:          false,
			gdprDefaultValue:   "1",
			expectPrivacyLabels: metrics.PrivacyLabels{
				GDPREnforced:   true,
				GDPRTCFVersion: "",
			},
		},
		{
			description:        "Enforce",
			gdprAccountEnabled: &trueValue,
			gdprHostEnabled:    true,
			gdpr:               "1",
			gdprConsent:        tcf2Consent,
			gdprScrub:          true,
			gdprDefaultValue:   "1",
			expectPrivacyLabels: metrics.PrivacyLabels{
				GDPREnforced:   true,
				GDPRTCFVersion: metrics.TCFVersionV2,
			},
		},
		{
			description:        "Not Enforce",
			gdprAccountEnabled: &trueValue,
			gdprHostEnabled:    true,
			gdpr:               "0",
			gdprConsent:        tcf2Consent,
			gdprScrub:          false,
			gdprDefaultValue:   "1",
			expectPrivacyLabels: metrics.PrivacyLabels{
				GDPREnforced:   false,
				GDPRTCFVersion: "",
			},
		},
		{
			description:        "Enforce; GDPR signal extraction error",
			gdprAccountEnabled: &trueValue,
			gdprHostEnabled:    true,
			gdpr:               "0{",
			gdprConsent:        tcf2Consent,
			gdprScrub:          true,
			gdprDefaultValue:   "1",
			expectPrivacyLabels: metrics.PrivacyLabels{
				GDPREnforced:   true,
				GDPRTCFVersion: metrics.TCFVersionV2,
			},
			expectError: true,
		},
		{
			description:        "Enforce; account GDPR enabled, host GDPR setting disregarded",
			gdprAccountEnabled: &trueValue,
			gdprHostEnabled:    false,
			gdpr:               "1",
			gdprConsent:        tcf2Consent,
			gdprScrub:          true,
			gdprDefaultValue:   "1",
			expectPrivacyLabels: metrics.PrivacyLabels{
				GDPREnforced:   true,
				GDPRTCFVersion: metrics.TCFVersionV2,
			},
		},
		{
			description:        "Not Enforce; account GDPR disabled, host GDPR setting disregarded",
			gdprAccountEnabled: &falseValue,
			gdprHostEnabled:    true,
			gdpr:               "1",
			gdprConsent:        tcf2Consent,
			gdprScrub:          false,
			gdprDefaultValue:   "1",
			expectPrivacyLabels: metrics.PrivacyLabels{
				GDPREnforced:   false,
				GDPRTCFVersion: "",
			},
		},
		{
			description:        "Enforce; account GDPR not specified, host GDPR enabled",
			gdprAccountEnabled: nil,
			gdprHostEnabled:    true,
			gdpr:               "1",
			gdprConsent:        tcf2Consent,
			gdprScrub:          true,
			gdprDefaultValue:   "1",
			expectPrivacyLabels: metrics.PrivacyLabels{
				GDPREnforced:   true,
				GDPRTCFVersion: metrics.TCFVersionV2,
			},
		},
		{
			description:        "Not Enforce; account GDPR not specified, host GDPR disabled",
			gdprAccountEnabled: nil,
			gdprHostEnabled:    false,
			gdpr:               "1",
			gdprConsent:        tcf2Consent,
			gdprScrub:          false,
			gdprDefaultValue:   "1",
			expectPrivacyLabels: metrics.PrivacyLabels{
				GDPREnforced:   false,
				GDPRTCFVersion: "",
			},
		},
		{
			description:        "Enforce - Ambiguous signal, don't sync user if ambiguous",
			gdprAccountEnabled: nil,
			gdprHostEnabled:    true,
			gdpr:               "null",
			gdprConsent:        tcf2Consent,
			gdprScrub:          true,
			gdprDefaultValue:   "1",
			expectPrivacyLabels: metrics.PrivacyLabels{
				GDPREnforced:   true,
				GDPRTCFVersion: metrics.TCFVersionV2,
			},
		},
		{
			description:        "Not Enforce - Ambiguous signal, sync user if ambiguous",
			gdprAccountEnabled: nil,
			gdprHostEnabled:    true,
			gdpr:               "null",
			gdprConsent:        tcf2Consent,
			gdprScrub:          false,
			gdprDefaultValue:   "0",
			expectPrivacyLabels: metrics.PrivacyLabels{
				GDPREnforced:   false,
				GDPRTCFVersion: "",
			},
		},
		{
			description:        "Enforce - error while checking if personal info is allowed",
			gdprAccountEnabled: nil,
			gdprHostEnabled:    true,
			gdpr:               "1",
			gdprConsent:        tcf2Consent,
			gdprScrub:          true,
			permissionsError:   errors.New("Some error"),
			gdprDefaultValue:   "1",
			expectPrivacyLabels: metrics.PrivacyLabels{
				GDPREnforced:   true,
				GDPRTCFVersion: metrics.TCFVersionV2,
			},
		},
	}

	for _, test := range testCases {
		req := newBidRequest(t)
		req.User.Ext = json.RawMessage(`{"consent":"` + test.gdprConsent + `"}`)
		req.Regs = &openrtb2.Regs{
			Ext: json.RawMessage(`{"gdpr":` + test.gdpr + `}`),
		}

		privacyConfig := config.Privacy{
			GDPR: config.GDPR{
				DefaultValue: test.gdprDefaultValue,
				TCF2: config.TCF2{
					Enabled: test.gdprHostEnabled,
				},
			},
		}

		accountConfig := config.Account{
			GDPR: config.AccountGDPR{
				Enabled: test.gdprAccountEnabled,
			},
		}

		auctionReq := AuctionRequest{
			BidRequestWrapper: &openrtb_ext.RequestWrapper{BidRequest: req},
			UserSyncs:         &emptyUsersync{},
			Account:           accountConfig,
		}

		gdprPermissionsBuilder := fakePermissionsBuilder{
			permissions: &permissionsMock{
				allowAllBidders: true,
				passGeo:         !test.gdprScrub,
				passID:          !test.gdprScrub,
				activitiesError: test.permissionsError,
			},
		}.Builder
		tcf2ConfigBuilder := fakeTCF2ConfigBuilder{
			cfg: gdpr.NewTCF2Config(
				privacyConfig.GDPR.TCF2,
				accountConfig.GDPR,
			),
		}.Builder

		gdprDefaultValue := gdpr.SignalYes
		if test.gdprDefaultValue == "0" {
			gdprDefaultValue = gdpr.SignalNo
		}

		reqSplitter := &requestSplitter{
			bidderToSyncerKey: map[string]string{},
			me:                &metrics.MetricsEngineMock{},
			privacyConfig:     privacyConfig,
			gdprPermsBuilder:  gdprPermissionsBuilder,
			tcf2ConfigBuilder: tcf2ConfigBuilder,
			hostSChainNode:    nil,
			bidderInfo:        config.BidderInfos{},
		}

		results, privacyLabels, errs := reqSplitter.cleanOpenRTBRequests(context.Background(), auctionReq, nil, gdprDefaultValue)
		result := results[0]

		if test.expectError {
			assert.NotNil(t, errs)
		} else {
			assert.Nil(t, errs)
		}

		if test.gdprScrub {
			assert.Equal(t, result.BidRequest.User.BuyerUID, "", test.description+":User.BuyerUID")
			assert.Equal(t, result.BidRequest.Device.DIDMD5, "", test.description+":Device.DIDMD5")
		} else {
			assert.NotEqual(t, result.BidRequest.User.BuyerUID, "", test.description+":User.BuyerUID")
			assert.NotEqual(t, result.BidRequest.Device.DIDMD5, "", test.description+":Device.DIDMD5")
		}
		assert.Equal(t, test.expectPrivacyLabels, privacyLabels, test.description+":PrivacyLabels")
	}
}

func TestCleanOpenRTBRequestsGDPRBlockBidRequest(t *testing.T) {
	testCases := []struct {
		description            string
		gdprEnforced           bool
		gdprAllowedBidders     []openrtb_ext.BidderName
		expectedBidders        []openrtb_ext.BidderName
		expectedBlockedBidders []openrtb_ext.BidderName
	}{
		{
			description:            "gdpr enforced, one request allowed and one request blocked",
			gdprEnforced:           true,
			gdprAllowedBidders:     []openrtb_ext.BidderName{openrtb_ext.BidderAppnexus},
			expectedBidders:        []openrtb_ext.BidderName{openrtb_ext.BidderAppnexus},
			expectedBlockedBidders: []openrtb_ext.BidderName{openrtb_ext.BidderRubicon},
		},
		{
			description:            "gdpr enforced, two requests allowed and no requests blocked",
			gdprEnforced:           true,
			gdprAllowedBidders:     []openrtb_ext.BidderName{openrtb_ext.BidderAppnexus, openrtb_ext.BidderRubicon},
			expectedBidders:        []openrtb_ext.BidderName{openrtb_ext.BidderAppnexus, openrtb_ext.BidderRubicon},
			expectedBlockedBidders: []openrtb_ext.BidderName{},
		},
		{
			description:            "gdpr not enforced, two requests allowed and no requests blocked",
			gdprEnforced:           false,
			gdprAllowedBidders:     []openrtb_ext.BidderName{},
			expectedBidders:        []openrtb_ext.BidderName{openrtb_ext.BidderAppnexus, openrtb_ext.BidderRubicon},
			expectedBlockedBidders: []openrtb_ext.BidderName{},
		},
	}

	for _, test := range testCases {
		req := newBidRequest(t)
		req.Regs = &openrtb2.Regs{
			Ext: json.RawMessage(`{"gdpr":1}`),
		}
		req.Imp[0].Ext = json.RawMessage(`{"prebid":{"bidder":{"appnexus": {"placementId": 1}, "rubicon": {}}}}`)

		privacyConfig := config.Privacy{
			GDPR: config.GDPR{
				DefaultValue: "0",
				TCF2: config.TCF2{
					Enabled: test.gdprEnforced,
				},
			},
		}

		accountConfig := config.Account{
			GDPR: config.AccountGDPR{
				Enabled: nil,
			},
		}

		auctionReq := AuctionRequest{
			BidRequestWrapper: &openrtb_ext.RequestWrapper{BidRequest: req},
			UserSyncs:         &emptyUsersync{},
			Account:           accountConfig,
		}

		gdprPermissionsBuilder := fakePermissionsBuilder{
			permissions: &permissionsMock{
				allowedBidders:  test.gdprAllowedBidders,
				passGeo:         true,
				passID:          true,
				activitiesError: nil,
			},
		}.Builder
		tcf2ConfigBuilder := fakeTCF2ConfigBuilder{
			cfg: gdpr.NewTCF2Config(privacyConfig.GDPR.TCF2, accountConfig.GDPR),
		}.Builder

		metricsMock := metrics.MetricsEngineMock{}
		metricsMock.Mock.On("RecordAdapterGDPRRequestBlocked", mock.Anything).Return()

		reqSplitter := &requestSplitter{
			bidderToSyncerKey: map[string]string{},
			me:                &metricsMock,
			privacyConfig:     privacyConfig,
			gdprPermsBuilder:  gdprPermissionsBuilder,
			tcf2ConfigBuilder: tcf2ConfigBuilder,
			hostSChainNode:    nil,
			bidderInfo:        config.BidderInfos{},
		}

		results, _, errs := reqSplitter.cleanOpenRTBRequests(context.Background(), auctionReq, nil, gdpr.SignalNo)

		// extract bidder name from each request in the results
		bidders := []openrtb_ext.BidderName{}
		for _, req := range results {
			bidders = append(bidders, req.BidderName)
		}

		assert.Empty(t, errs, test.description)
		assert.ElementsMatch(t, bidders, test.expectedBidders, test.description)

		for _, blockedBidder := range test.expectedBlockedBidders {
			metricsMock.AssertCalled(t, "RecordAdapterGDPRRequestBlocked", blockedBidder)
		}
		for _, allowedBidder := range test.expectedBidders {
			metricsMock.AssertNotCalled(t, "RecordAdapterGDPRRequestBlocked", allowedBidder)
		}
	}
}

func TestBuildRequestExtForBidder(t *testing.T) {
	bidder := "foo"
	bidderParams := json.RawMessage(`"bar"`)

	testCases := []struct {
		description          string
		requestExt           json.RawMessage
		bidderParams         map[string]json.RawMessage
		alternateBidderCodes *openrtb_ext.ExtAlternateBidderCodes
		expectedJson         json.RawMessage
	}{
		{
			description:          "Nil",
			bidderParams:         nil,
			requestExt:           nil,
			alternateBidderCodes: nil,
			expectedJson:         json.RawMessage(``),
		},
		{
			description:          "Empty",
			bidderParams:         nil,
			alternateBidderCodes: nil,
			requestExt:           json.RawMessage(`{}`),
			expectedJson:         json.RawMessage(`{"prebid":{}}`),
		},
		{
			description:  "Prebid - Allowed Fields Only",
			bidderParams: nil,
			requestExt:   json.RawMessage(`{"prebid":{"integration":"a","channel":{"name":"b","version":"c"},"debug":true,"currency":{"rates":{"FOO":{"BAR":42}},"usepbsrates":true}, "server": {"externalurl": "url", "gvlid": 1, "datacenter": "2"}}}`),
			expectedJson: json.RawMessage(`{"prebid":{"integration":"a","channel":{"name":"b","version":"c"},"debug":true,"currency":{"rates":{"FOO":{"BAR":42}},"usepbsrates":true}, "server": {"externalurl": "url", "gvlid": 1, "datacenter": "2"}}}`),
		},
		{
			description:  "Prebid - Allowed Fields + Bidder Params",
			bidderParams: map[string]json.RawMessage{bidder: bidderParams},
			requestExt:   json.RawMessage(`{"prebid":{"integration":"a","channel":{"name":"b","version":"c"},"debug":true,"currency":{"rates":{"FOO":{"BAR":42}},"usepbsrates":true}, "server": {"externalurl": "url", "gvlid": 1, "datacenter": "2"}}}`),
			expectedJson: json.RawMessage(`{"prebid":{"integration":"a","channel":{"name":"b","version":"c"},"debug":true,"currency":{"rates":{"FOO":{"BAR":42}},"usepbsrates":true}, "server": {"externalurl": "url", "gvlid": 1, "datacenter": "2"}, "bidderparams":"bar"}}`),
		},
		{
			description:  "Other",
			bidderParams: nil,
			requestExt:   json.RawMessage(`{"other":"foo"}`),
			expectedJson: json.RawMessage(`{"other":"foo","prebid":{}}`),
		},
		{
			description:  "Prebid + Other + Bider Params",
			bidderParams: map[string]json.RawMessage{bidder: bidderParams},
			requestExt:   json.RawMessage(`{"other":"foo","prebid":{"integration":"a","channel":{"name":"b","version":"c"},"debug":true,"currency":{"rates":{"FOO":{"BAR":42}},"usepbsrates":true}, "server": {"externalurl": "url", "gvlid": 1, "datacenter": "2"}}}`),
			expectedJson: json.RawMessage(`{"other":"foo","prebid":{"integration":"a","channel":{"name":"b","version":"c"},"debug":true,"currency":{"rates":{"FOO":{"BAR":42}},"usepbsrates":true}, "server": {"externalurl": "url", "gvlid": 1, "datacenter": "2"}, "bidderparams":"bar"}}`),
		},
		{
			description:          "Prebid + AlternateBidderCodes in pbs config (default explicitly defined)",
			bidderParams:         map[string]json.RawMessage{bidder: bidderParams},
			alternateBidderCodes: &openrtb_ext.ExtAlternateBidderCodes{},
			requestExt:           json.RawMessage(`{"other":"foo","prebid":{"integration":"a","channel":{"name":"b","version":"c"},"debug":true,"currency":{"rates":{"FOO":{"BAR":42}},"usepbsrates":true}}}`),
			expectedJson:         json.RawMessage(`{"other":"foo","prebid":{"alternatebiddercodes":{"enabled":false,"bidders":null},"integration":"a","channel":{"name":"b","version":"c"},"debug":true,"currency":{"rates":{"FOO":{"BAR":42}},"usepbsrates":true},"bidderparams":"bar"}}`),
		},
		{
			description:          "Prebid + AlternateBidderCodes in pbs config",
			bidderParams:         map[string]json.RawMessage{bidder: bidderParams},
			alternateBidderCodes: &openrtb_ext.ExtAlternateBidderCodes{Enabled: true, Bidders: map[string]openrtb_ext.ExtAdapterAlternateBidderCodes{"foo": {Enabled: true, AllowedBidderCodes: []string{"*"}}}},
			requestExt:           json.RawMessage(`{"other":"foo"}`),
			expectedJson:         json.RawMessage(`{"other":"foo","prebid":{"alternatebiddercodes":{"enabled":true,"bidders":{"foo":{"enabled":true,"allowedbiddercodes":["*"]}}},"bidderparams":"bar"}}`),
		},
		{
			description:          "Prebid + AlternateBidderCodes in pbs config but current bidder not in AlternateBidderCodes config",
			bidderParams:         map[string]json.RawMessage{bidder: bidderParams},
			alternateBidderCodes: &openrtb_ext.ExtAlternateBidderCodes{Enabled: true, Bidders: map[string]openrtb_ext.ExtAdapterAlternateBidderCodes{"bar": {Enabled: true, AllowedBidderCodes: []string{"*"}}}},
			requestExt:           json.RawMessage(`{"other":"foo"}`),
			expectedJson:         json.RawMessage(`{"other":"foo","prebid":{"alternatebiddercodes":{"enabled":true,"bidders":null},"bidderparams":"bar"}}`),
		},
		{
			description:          "Prebid + AlternateBidderCodes in request",
			bidderParams:         map[string]json.RawMessage{bidder: bidderParams},
			alternateBidderCodes: &openrtb_ext.ExtAlternateBidderCodes{},
			requestExt:           json.RawMessage(`{"other":"foo","prebid":{"integration":"a","channel":{"name":"b","version":"c"},"debug":true,"currency":{"rates":{"FOO":{"BAR":42}},"usepbsrates":true},"alternatebiddercodes":{"enabled":true,"bidders":{"foo":{"enabled":true,"allowedbiddercodes":["foo2"]},"bar":{"enabled":true,"allowedbiddercodes":["ix"]}}}}}`),
			expectedJson:         json.RawMessage(`{"other":"foo","prebid":{"integration":"a","channel":{"name":"b","version":"c"},"debug":true,"currency":{"rates":{"FOO":{"BAR":42}},"usepbsrates":true},"alternatebiddercodes":{"enabled":true,"bidders":{"foo":{"enabled":true,"allowedbiddercodes":["foo2"]}}},"bidderparams":"bar"}}`),
		},
		{
			description:          "Prebid + AlternateBidderCodes in request but current bidder not in AlternateBidderCodes config",
			bidderParams:         map[string]json.RawMessage{bidder: bidderParams},
			alternateBidderCodes: &openrtb_ext.ExtAlternateBidderCodes{},
			requestExt:           json.RawMessage(`{"other":"foo","prebid":{"integration":"a","channel":{"name":"b","version":"c"},"debug":true,"currency":{"rates":{"FOO":{"BAR":42}},"usepbsrates":true},"alternatebiddercodes":{"enabled":true,"bidders":{"bar":{"enabled":true,"allowedbiddercodes":["ix"]}}}}}`),
			expectedJson:         json.RawMessage(`{"other":"foo","prebid":{"integration":"a","channel":{"name":"b","version":"c"},"debug":true,"currency":{"rates":{"FOO":{"BAR":42}},"usepbsrates":true},"alternatebiddercodes":{"enabled":true,"bidders":null},"bidderparams":"bar"}}`),
		},
		{
			description:          "Prebid + AlternateBidderCodes in both pbs config and in the request",
			bidderParams:         map[string]json.RawMessage{bidder: bidderParams},
			alternateBidderCodes: &openrtb_ext.ExtAlternateBidderCodes{Enabled: true, Bidders: map[string]openrtb_ext.ExtAdapterAlternateBidderCodes{"foo": {Enabled: true, AllowedBidderCodes: []string{"*"}}}},
			requestExt:           json.RawMessage(`{"other":"foo","prebid":{"integration":"a","channel":{"name":"b","version":"c"},"debug":true,"currency":{"rates":{"FOO":{"BAR":42}},"usepbsrates":true},"alternatebiddercodes":{"enabled":true,"bidders":{"foo":{"enabled":true,"allowedbiddercodes":["foo2"]},"bar":{"enabled":true,"allowedbiddercodes":["ix"]}}}}}`),
			expectedJson:         json.RawMessage(`{"other":"foo","prebid":{"integration":"a","channel":{"name":"b","version":"c"},"debug":true,"currency":{"rates":{"FOO":{"BAR":42}},"usepbsrates":true},"alternatebiddercodes":{"enabled":true,"bidders":{"foo":{"enabled":true,"allowedbiddercodes":["foo2"]}}},"bidderparams":"bar"}}`),
		},
	}

	for _, test := range testCases {
		requestExtParsed := &openrtb_ext.ExtRequest{}
		if test.requestExt != nil {
			err := json.Unmarshal(test.requestExt, requestExtParsed)
			if !assert.NoError(t, err, test.description+":parse_ext") {
				continue
			}
		}

		actualJson, actualErr := buildRequestExtForBidder(bidder, test.requestExt, requestExtParsed, test.bidderParams, test.alternateBidderCodes)
		if len(test.expectedJson) > 0 {
			assert.JSONEq(t, string(test.expectedJson), string(actualJson), test.description+":json")
		} else {
			assert.Equal(t, test.expectedJson, actualJson, test.description+":json")
		}
		assert.NoError(t, actualErr, test.description+":err")
	}
}

func TestBuildRequestExtForBidder_RequestExtParsedNil(t *testing.T) {
	var (
		bidder               = "foo"
		requestExt           = json.RawMessage(`{}`)
		requestExtParsed     *openrtb_ext.ExtRequest
		bidderParams         map[string]json.RawMessage
		alternateBidderCodes *openrtb_ext.ExtAlternateBidderCodes
	)

	actualJson, actualErr := buildRequestExtForBidder(bidder, requestExt, requestExtParsed, bidderParams, alternateBidderCodes)
	assert.Equal(t, json.RawMessage(``), actualJson)
	assert.NoError(t, actualErr)
}

func TestBuildRequestExtForBidder_RequestExtMalformed(t *testing.T) {
	var (
		bidder               = "foo"
		requestExt           = json.RawMessage(`malformed`)
		requestExtParsed     = &openrtb_ext.ExtRequest{}
		bidderParams         map[string]json.RawMessage
		alternateBidderCodes *openrtb_ext.ExtAlternateBidderCodes
	)

	actualJson, actualErr := buildRequestExtForBidder(bidder, requestExt, requestExtParsed, bidderParams, alternateBidderCodes)
	assert.Equal(t, json.RawMessage(nil), actualJson)
	assert.EqualError(t, actualErr, "invalid character 'm' looking for beginning of value")
}

// newAdapterAliasBidRequest builds a BidRequest with aliases
func newAdapterAliasBidRequest(t *testing.T) *openrtb2.BidRequest {
	dnt := int8(1)
	return &openrtb2.BidRequest{
		Site: &openrtb2.Site{
			Page:   "www.some.domain.com",
			Domain: "domain.com",
			Publisher: &openrtb2.Publisher{
				ID: "some-publisher-id",
			},
		},
		Device: &openrtb2.Device{
			DIDMD5:   "some device ID hash",
			UA:       "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_13_5) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/67.0.3396.87 Safari/537.36",
			IFA:      "ifa",
			IP:       "132.173.230.74",
			DNT:      &dnt,
			Language: "EN",
		},
		Source: &openrtb2.Source{
			TID: "61018dc9-fa61-4c41-b7dc-f90b9ae80e87",
		},
		User: &openrtb2.User{
			ID:       "our-id",
			BuyerUID: "their-id",
			Ext:      json.RawMessage(`{"consent":"BONciguONcjGKADACHENAOLS1rAHDAFAAEAASABQAMwAeACEAFw"}`),
		},
		Regs: &openrtb2.Regs{
			Ext: json.RawMessage(`{"gdpr":1}`),
		},
		Imp: []openrtb2.Imp{{
			ID: "some-imp-id",
			Banner: &openrtb2.Banner{
				Format: []openrtb2.Format{{
					W: 300,
					H: 250,
				}, {
					W: 300,
					H: 600,
				}},
			},
			Ext: json.RawMessage(`{"appnexus": {"placementId": 1},"brightroll": {"placementId": 105}}`),
		}},
		Ext: json.RawMessage(`{"prebid":{"aliases":{"brightroll":"appnexus"}}}`),
	}
}

func newBidRequest(t *testing.T) *openrtb2.BidRequest {
	return &openrtb2.BidRequest{
		Site: &openrtb2.Site{
			Page:   "www.some.domain.com",
			Domain: "domain.com",
			Publisher: &openrtb2.Publisher{
				ID: "some-publisher-id",
			},
		},
		Device: &openrtb2.Device{
			DIDMD5:   "some device ID hash",
			UA:       "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_13_5) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/67.0.3396.87 Safari/537.36",
			IFA:      "ifa",
			IP:       "132.173.230.74",
			Language: "EN",
		},
		Source: &openrtb2.Source{
			TID: "61018dc9-fa61-4c41-b7dc-f90b9ae80e87",
		},
		User: &openrtb2.User{
			ID:       "our-id",
			BuyerUID: "their-id",
			Yob:      1982,
			Ext:      json.RawMessage(`{}`),
		},
		Imp: []openrtb2.Imp{{
			ID: "some-imp-id",
			Banner: &openrtb2.Banner{
				Format: []openrtb2.Format{{
					W: 300,
					H: 250,
				}, {
					W: 300,
					H: 600,
				}},
			},
			Ext: json.RawMessage(`{"prebid":{"bidder":{"appnexus": {"placementId": 1}}}}`),
		}},
	}
}

func newBidRequestWithBidderParams(t *testing.T) *openrtb2.BidRequest {
	return &openrtb2.BidRequest{
		Site: &openrtb2.Site{
			Page:   "www.some.domain.com",
			Domain: "domain.com",
			Publisher: &openrtb2.Publisher{
				ID: "some-publisher-id",
			},
		},
		Device: &openrtb2.Device{
			DIDMD5:   "some device ID hash",
			UA:       "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_13_5) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/67.0.3396.87 Safari/537.36",
			IFA:      "ifa",
			IP:       "132.173.230.74",
			Language: "EN",
		},
		Source: &openrtb2.Source{
			TID: "61018dc9-fa61-4c41-b7dc-f90b9ae80e87",
		},
		User: &openrtb2.User{
			ID:       "our-id",
			BuyerUID: "their-id",
			Yob:      1982,
			Ext:      json.RawMessage(`{}`),
		},
		Imp: []openrtb2.Imp{{
			ID: "some-imp-id",
			Banner: &openrtb2.Banner{
				Format: []openrtb2.Format{{
					W: 300,
					H: 250,
				}, {
					W: 300,
					H: 600,
				}},
			},
			Ext: json.RawMessage(`{"prebid":{"bidder":{"appnexus": {"placementId": 1}, "pubmatic":{"publisherId": "1234"}}}}`),
		}},
	}
}

func TestRandomizeList(t *testing.T) {
	var (
		bidder1 = openrtb_ext.BidderName("bidder1")
		bidder2 = openrtb_ext.BidderName("bidder2")
		bidder3 = openrtb_ext.BidderName("bidder3")
	)

	testCases := []struct {
		description string
		bidders     []openrtb_ext.BidderName
	}{
		{
			description: "None",
			bidders:     []openrtb_ext.BidderName{},
		},
		{
			description: "One",
			bidders:     []openrtb_ext.BidderName{bidder1},
		},
		{
			description: "Many",
			bidders:     []openrtb_ext.BidderName{bidder1, bidder2, bidder3},
		},
	}

	for _, test := range testCases {
		biddersWorkingCopy := make([]openrtb_ext.BidderName, len(test.bidders))
		copy(biddersWorkingCopy, test.bidders)

		randomizeList(biddersWorkingCopy)

		// test all bidders are still present, ignoring order. we are testing the algorithm doesn't loose
		// elements. we are not testing the random number generator itself.
		assert.ElementsMatch(t, test.bidders, biddersWorkingCopy)
	}
}

func TestRemoveUnpermissionedEids(t *testing.T) {
	bidder := "bidderA"

	testCases := []struct {
		description     string
		userExt         json.RawMessage
		eidPermissions  []openrtb_ext.ExtRequestPrebidDataEidPermission
		expectedUserExt json.RawMessage
	}{
		{
			description: "Extension Nil",
			userExt:     nil,
			eidPermissions: []openrtb_ext.ExtRequestPrebidDataEidPermission{
				{Source: "source1", Bidders: []string{"bidderA"}},
			},
			expectedUserExt: nil,
		},
		{
			description: "Extension Empty",
			userExt:     json.RawMessage(`{}`),
			eidPermissions: []openrtb_ext.ExtRequestPrebidDataEidPermission{
				{Source: "source1", Bidders: []string{"bidderA"}},
			},
			expectedUserExt: json.RawMessage(`{}`),
		},
		{
			description: "Extension Empty - Keep Other Data",
			userExt:     json.RawMessage(`{"other":42}`),
			eidPermissions: []openrtb_ext.ExtRequestPrebidDataEidPermission{
				{Source: "source1", Bidders: []string{"bidderA"}},
			},
			expectedUserExt: json.RawMessage(`{"other":42}`),
		},
		{
			description: "Eids Empty",
			userExt:     json.RawMessage(`{"eids":[]}`),
			eidPermissions: []openrtb_ext.ExtRequestPrebidDataEidPermission{
				{Source: "source1", Bidders: []string{"bidderA"}},
			},
			expectedUserExt: json.RawMessage(`{"eids":[]}`),
		},
		{
			description: "Eids Empty - Keep Other Data",
			userExt:     json.RawMessage(`{"eids":[],"other":42}`),
			eidPermissions: []openrtb_ext.ExtRequestPrebidDataEidPermission{
				{Source: "source1", Bidders: []string{"bidderA"}},
			},
			expectedUserExt: json.RawMessage(`{"eids":[],"other":42}`),
		},
		{
			description:     "Allowed By Nil Permissions",
			userExt:         json.RawMessage(`{"eids":[{"source":"source1","uids":[{"id":"anyID"}]}]}`),
			eidPermissions:  nil,
			expectedUserExt: json.RawMessage(`{"eids":[{"source":"source1","uids":[{"id":"anyID"}]}]}`),
		},
		{
			description:     "Allowed By Empty Permissions",
			userExt:         json.RawMessage(`{"eids":[{"source":"source1","uids":[{"id":"anyID"}]}]}`),
			eidPermissions:  []openrtb_ext.ExtRequestPrebidDataEidPermission{},
			expectedUserExt: json.RawMessage(`{"eids":[{"source":"source1","uids":[{"id":"anyID"}]}]}`),
		},
		{
			description: "Allowed By Specific Bidder",
			userExt:     json.RawMessage(`{"eids":[{"source":"source1","uids":[{"id":"anyID"}]}]}`),
			eidPermissions: []openrtb_ext.ExtRequestPrebidDataEidPermission{
				{Source: "source1", Bidders: []string{"bidderA"}},
			},
			expectedUserExt: json.RawMessage(`{"eids":[{"source":"source1","uids":[{"id":"anyID"}]}]}`),
		},
		{
			description: "Allowed By All Bidders",
			userExt:     json.RawMessage(`{"eids":[{"source":"source1","uids":[{"id":"anyID"}]}]}`),
			eidPermissions: []openrtb_ext.ExtRequestPrebidDataEidPermission{
				{Source: "source1", Bidders: []string{"*"}},
			},
			expectedUserExt: json.RawMessage(`{"eids":[{"source":"source1","uids":[{"id":"anyID"}]}]}`),
		},
		{
			description: "Allowed By Lack Of Matching Source",
			userExt:     json.RawMessage(`{"eids":[{"source":"source1","uids":[{"id":"anyID"}]}]}`),
			eidPermissions: []openrtb_ext.ExtRequestPrebidDataEidPermission{
				{Source: "source2", Bidders: []string{"otherBidder"}},
			},
			expectedUserExt: json.RawMessage(`{"eids":[{"source":"source1","uids":[{"id":"anyID"}]}]}`),
		},
		{
			description: "Allowed - Keep Other Data",
			userExt:     json.RawMessage(`{"eids":[{"source":"source1","uids":[{"id":"anyID"}]}],"other":42}`),
			eidPermissions: []openrtb_ext.ExtRequestPrebidDataEidPermission{
				{Source: "source1", Bidders: []string{"bidderA"}},
			},
			expectedUserExt: json.RawMessage(`{"eids":[{"source":"source1","uids":[{"id":"anyID"}]}],"other":42}`),
		},
		{
			description: "Denied",
			userExt:     json.RawMessage(`{"eids":[{"source":"source1","uids":[{"id":"anyID"}]}]}`),
			eidPermissions: []openrtb_ext.ExtRequestPrebidDataEidPermission{
				{Source: "source1", Bidders: []string{"otherBidder"}},
			},
			expectedUserExt: nil,
		},
		{
			description: "Denied - Keep Other Data",
			userExt:     json.RawMessage(`{"eids":[{"source":"source1","uids":[{"id":"anyID"}]}],"otherdata":42}`),
			eidPermissions: []openrtb_ext.ExtRequestPrebidDataEidPermission{
				{Source: "source1", Bidders: []string{"otherBidder"}},
			},
			expectedUserExt: json.RawMessage(`{"otherdata":42}`),
		},
		{
			description: "Mix Of Allowed By Specific Bidder, Allowed By Lack Of Matching Source, Denied, Keep Other Data",
			userExt:     json.RawMessage(`{"eids":[{"source":"source1","uids":[{"id":"anyID1"}]},{"source":"source2","uids":[{"id":"anyID2"}]},{"source":"source3","uids":[{"id":"anyID3"}]}],"other":42}`),
			eidPermissions: []openrtb_ext.ExtRequestPrebidDataEidPermission{
				{Source: "source1", Bidders: []string{"bidderA"}},
				{Source: "source3", Bidders: []string{"otherBidder"}},
			},
			expectedUserExt: json.RawMessage(`{"eids":[{"source":"source1","uids":[{"id":"anyID1"}]},{"source":"source2","uids":[{"id":"anyID2"}]}],"other":42}`),
		},
	}

	for _, test := range testCases {
		request := &openrtb2.BidRequest{
			User: &openrtb2.User{Ext: test.userExt},
		}

		requestExt := &openrtb_ext.ExtRequest{
			Prebid: openrtb_ext.ExtRequestPrebid{
				Data: &openrtb_ext.ExtRequestPrebidData{
					EidPermissions: test.eidPermissions,
				},
			},
		}

		expectedRequest := &openrtb2.BidRequest{
			User: &openrtb2.User{Ext: test.expectedUserExt},
		}

		resultErr := removeUnpermissionedEids(request, bidder, requestExt)
		assert.NoError(t, resultErr, test.description)
		assert.Equal(t, expectedRequest, request, test.description)
	}
}

func TestRemoveUnpermissionedEidsUnmarshalErrors(t *testing.T) {
	testCases := []struct {
		description string
		userExt     json.RawMessage
		expectedErr string
	}{
		{
			description: "Malformed Ext",
			userExt:     json.RawMessage(`malformed`),
			expectedErr: "invalid character 'm' looking for beginning of value",
		},
		{
			description: "Malformed Eid Array Type",
			userExt:     json.RawMessage(`{"eids":[42]}`),
			expectedErr: "json: cannot unmarshal number into Go value of type openrtb2.EID",
		},
		{
			description: "Malformed Eid Item Type",
			userExt:     json.RawMessage(`{"eids":[{"source":42,"id":"anyID"}]}`),
			expectedErr: "json: cannot unmarshal number into Go struct field EID.source of type string",
		},
	}

	for _, test := range testCases {
		request := &openrtb2.BidRequest{
			User: &openrtb2.User{Ext: test.userExt},
		}

		requestExt := &openrtb_ext.ExtRequest{
			Prebid: openrtb_ext.ExtRequestPrebid{
				Data: &openrtb_ext.ExtRequestPrebidData{
					EidPermissions: []openrtb_ext.ExtRequestPrebidDataEidPermission{
						{Source: "source1", Bidders: []string{"*"}},
					},
				},
			},
		}

		resultErr := removeUnpermissionedEids(request, "bidderA", requestExt)
		assert.EqualError(t, resultErr, test.expectedErr, test.description)
	}
}

func TestGetDebugInfo(t *testing.T) {
	type testInput struct {
		debugEnabledOrOverridden bool
		accountDebugFlag         bool
	}
	type testOut struct {
		responseDebugAllow bool
		accountDebugAllow  bool
		debugLog           *DebugLog
	}
	type testCase struct {
		in       testInput
		expected testOut
	}

	testGroups := []struct {
		desc   string
		bidReq *openrtb2.BidRequest
		tests  []testCase
	}{
		{
			"Bid request doesn't call for debug info",
			&openrtb2.BidRequest{Test: 0},
			[]testCase{
				{
					testInput{debugEnabledOrOverridden: false, accountDebugFlag: false},
					testOut{
						responseDebugAllow: false,
						accountDebugAllow:  false,
						debugLog:           &DebugLog{Enabled: false},
					},
				},
				{
					testInput{debugEnabledOrOverridden: false, accountDebugFlag: true},
					testOut{
						responseDebugAllow: false,
						accountDebugAllow:  false,
						debugLog:           &DebugLog{Enabled: true},
					},
				},
				{
					testInput{debugEnabledOrOverridden: true, accountDebugFlag: false},
					testOut{
						responseDebugAllow: true,
						accountDebugAllow:  false,
						debugLog:           &DebugLog{DebugEnabledOrOverridden: true, Enabled: true},
					},
				},
				{
					testInput{debugEnabledOrOverridden: true, accountDebugFlag: true},
					testOut{
						responseDebugAllow: true,
						accountDebugAllow:  true,
						debugLog:           &DebugLog{DebugEnabledOrOverridden: true, Enabled: true},
					},
				},
			},
		},
		{
			"Bid request requires debug info",
			&openrtb2.BidRequest{Test: 1},
			[]testCase{
				{
					testInput{debugEnabledOrOverridden: false, accountDebugFlag: false},
					testOut{
						responseDebugAllow: false,
						accountDebugAllow:  false,
						debugLog:           &DebugLog{Enabled: false},
					},
				},
				{
					testInput{debugEnabledOrOverridden: false, accountDebugFlag: true},
					testOut{
						responseDebugAllow: true,
						accountDebugAllow:  true,
						debugLog:           &DebugLog{Enabled: true},
					},
				},
				{
					testInput{debugEnabledOrOverridden: true, accountDebugFlag: false},
					testOut{
						responseDebugAllow: true,
						accountDebugAllow:  false,
						debugLog:           &DebugLog{DebugEnabledOrOverridden: true, Enabled: true},
					},
				},
				{
					testInput{debugEnabledOrOverridden: true, accountDebugFlag: true},
					testOut{
						responseDebugAllow: true,
						accountDebugAllow:  true,
						debugLog:           &DebugLog{DebugEnabledOrOverridden: true, Enabled: true},
					},
				},
			},
		},
	}
	for _, group := range testGroups {
		for i, tc := range group.tests {
			inDebugLog := &DebugLog{DebugEnabledOrOverridden: tc.in.debugEnabledOrOverridden}

			// run
			responseDebugAllow, accountDebugAllow, debugLog := getDebugInfo(group.bidReq, nil, tc.in.accountDebugFlag, inDebugLog)

			// assertions
			assert.Equal(t, tc.expected.responseDebugAllow, responseDebugAllow, "%s - %d", group.desc, i)
			assert.Equal(t, tc.expected.accountDebugAllow, accountDebugAllow, "%s - %d", group.desc, i)
			assert.Equal(t, tc.expected.debugLog, debugLog, "%s - %d", group.desc, i)
		}
	}
}

func TestRemoveUnpermissionedEidsEmptyValidations(t *testing.T) {
	testCases := []struct {
		description string
		request     *openrtb2.BidRequest
		requestExt  *openrtb_ext.ExtRequest
	}{
		{
			description: "Nil User",
			request: &openrtb2.BidRequest{
				User: nil,
			},
			requestExt: &openrtb_ext.ExtRequest{
				Prebid: openrtb_ext.ExtRequestPrebid{
					Data: &openrtb_ext.ExtRequestPrebidData{
						EidPermissions: []openrtb_ext.ExtRequestPrebidDataEidPermission{
							{Source: "source1", Bidders: []string{"*"}},
						},
					},
				},
			},
		},
		{
			description: "Empty User",
			request: &openrtb2.BidRequest{
				User: &openrtb2.User{},
			},
			requestExt: &openrtb_ext.ExtRequest{
				Prebid: openrtb_ext.ExtRequestPrebid{
					Data: &openrtb_ext.ExtRequestPrebidData{
						EidPermissions: []openrtb_ext.ExtRequestPrebidDataEidPermission{
							{Source: "source1", Bidders: []string{"*"}},
						},
					},
				},
			},
		},
		{
			description: "Nil Ext",
			request: &openrtb2.BidRequest{
				User: &openrtb2.User{Ext: json.RawMessage(`{"eids":[{"source":"source1","id":"anyID"}]}`)},
			},
			requestExt: nil,
		},
		{
			description: "Nil Prebid Data",
			request: &openrtb2.BidRequest{
				User: &openrtb2.User{Ext: json.RawMessage(`{"eids":[{"source":"source1","id":"anyID"}]}`)},
			},
			requestExt: &openrtb_ext.ExtRequest{
				Prebid: openrtb_ext.ExtRequestPrebid{
					Data: nil,
				},
			},
		},
	}

	for _, test := range testCases {
		requestExpected := *test.request

		resultErr := removeUnpermissionedEids(test.request, "bidderA", test.requestExt)
		assert.NoError(t, resultErr, test.description+":err")
		assert.Equal(t, &requestExpected, test.request, test.description+":request")
	}
}

func TestCleanOpenRTBRequestsSChainMultipleBidders(t *testing.T) {
	req := &openrtb2.BidRequest{
		Site: &openrtb2.Site{},
		Source: &openrtb2.Source{
			TID: "61018dc9-fa61-4c41-b7dc-f90b9ae80e87",
		},
		Imp: []openrtb2.Imp{{
			Ext: json.RawMessage(`{"prebid":{"bidder":{"appnexus": {"placementId": 1}, "axonix": { "supplyId": "123"}}}}`),
		}},
		Ext: json.RawMessage(`{"prebid":{"schains":[{ "bidders":["appnexus"],"schain":{"complete":1,"nodes":[{"asi":"directseller1.com","sid":"00001","rid":"BidRequest1","hp":1}],"ver":"1.0"}}, {"bidders":["axonix"],"schain":{"complete":1,"nodes":[{"asi":"directseller2.com","sid":"00002","rid":"BidRequest2","hp":1}],"ver":"1.0"}}]}}`),
	}

	unmarshaledExt, err := extractBidRequestExt(req)
	assert.NoErrorf(t, err, "Error unmarshaling inExt")
	extRequest := unmarshaledExt

	auctionReq := AuctionRequest{
		BidRequestWrapper: &openrtb_ext.RequestWrapper{BidRequest: req},
		UserSyncs:         &emptyUsersync{},
	}

	gdprPermissionsBuilder := fakePermissionsBuilder{
		permissions: &permissionsMock{
			allowAllBidders: true,
			passGeo:         true,
			passID:          true,
			activitiesError: nil,
		},
	}.Builder
	tcf2ConfigBuilder := fakeTCF2ConfigBuilder{
		cfg: gdpr.NewTCF2Config(config.TCF2{}, config.AccountGDPR{}),
	}.Builder

	reqSplitter := &requestSplitter{
		bidderToSyncerKey: map[string]string{},
		me:                &metrics.MetricsEngineMock{},
		privacyConfig:     config.Privacy{},
		gdprPermsBuilder:  gdprPermissionsBuilder,
		tcf2ConfigBuilder: tcf2ConfigBuilder,
		hostSChainNode:    nil,
		bidderInfo:        config.BidderInfos{},
	}
	bidderRequests, _, errs := reqSplitter.cleanOpenRTBRequests(context.Background(), auctionReq, extRequest, gdpr.SignalNo)

	assert.Nil(t, errs)
	assert.Len(t, bidderRequests, 2, "Bid request count is not 2")

	bidRequestSourceExts := map[openrtb_ext.BidderName]json.RawMessage{}
	for _, bidderRequest := range bidderRequests {
		bidRequestSourceExts[bidderRequest.BidderName] = bidderRequest.BidRequest.Source.Ext
	}

	appnexusPrebidSchainsSchain := json.RawMessage(`{"schain":{"complete":1,"nodes":[{"asi":"directseller1.com","sid":"00001","rid":"BidRequest1","hp":1}],"ver":"1.0"}}`)
	axonixPrebidSchainsSchain := json.RawMessage(`{"schain":{"complete":1,"nodes":[{"asi":"directseller2.com","sid":"00002","rid":"BidRequest2","hp":1}],"ver":"1.0"}}`)
	assert.Equal(t, appnexusPrebidSchainsSchain, bidRequestSourceExts["appnexus"], "Incorrect appnexus bid request schain in source.ext")
	assert.Equal(t, axonixPrebidSchainsSchain, bidRequestSourceExts["axonix"], "Incorrect axonix bid request schain in source.ext")
}

func TestApplyFPD(t *testing.T) {

	testCases := []struct {
		description     string
		inputFpd        firstpartydata.ResolvedFirstPartyData
		inputRequest    openrtb2.BidRequest
		expectedRequest openrtb2.BidRequest
	}{
		{
			description:     "req.Site defined; bidderFPD.Site not defined; expect request.Site remains the same",
			inputFpd:        firstpartydata.ResolvedFirstPartyData{Site: nil, App: nil, User: nil},
			inputRequest:    openrtb2.BidRequest{Site: &openrtb2.Site{ID: "SiteId"}},
			expectedRequest: openrtb2.BidRequest{Site: &openrtb2.Site{ID: "SiteId"}},
		},
		{
			description: "req.Site, req.App, req.User are not defined; bidderFPD.App, bidderFPD.Site and bidderFPD.User defined; " +
				"expect req.Site, req.App, req.User to be overriden by bidderFPD.App, bidderFPD.Site and bidderFPD.User",
			inputFpd:        firstpartydata.ResolvedFirstPartyData{Site: &openrtb2.Site{ID: "SiteId"}, App: &openrtb2.App{ID: "AppId"}, User: &openrtb2.User{ID: "UserId"}},
			inputRequest:    openrtb2.BidRequest{},
			expectedRequest: openrtb2.BidRequest{Site: &openrtb2.Site{ID: "SiteId"}, App: &openrtb2.App{ID: "AppId"}, User: &openrtb2.User{ID: "UserId"}},
		},
		{
			description:     "req.Site, defined; bidderFPD.App defined; expect request.App to be overriden by bidderFPD.App; expect req.Site remains the same",
			inputFpd:        firstpartydata.ResolvedFirstPartyData{App: &openrtb2.App{ID: "AppId"}},
			inputRequest:    openrtb2.BidRequest{Site: &openrtb2.Site{ID: "SiteId"}},
			expectedRequest: openrtb2.BidRequest{Site: &openrtb2.Site{ID: "SiteId"}, App: &openrtb2.App{ID: "AppId"}},
		},
		{
			description:     "req.Site, req.App defined; bidderFPD.App defined; expect request.App to be overriden by bidderFPD.App",
			inputFpd:        firstpartydata.ResolvedFirstPartyData{App: &openrtb2.App{ID: "AppId"}},
			inputRequest:    openrtb2.BidRequest{Site: &openrtb2.Site{ID: "SiteId"}, App: &openrtb2.App{ID: "TestAppId"}},
			expectedRequest: openrtb2.BidRequest{Site: &openrtb2.Site{ID: "SiteId"}, App: &openrtb2.App{ID: "AppId"}},
		},
		{
			description:     "req.User is defined; bidderFPD.User defined; req.User has BuyerUID. Expect to see user.BuyerUID in result request",
			inputFpd:        firstpartydata.ResolvedFirstPartyData{Site: &openrtb2.Site{ID: "SiteId"}, App: &openrtb2.App{ID: "AppId"}, User: &openrtb2.User{ID: "UserId"}},
			inputRequest:    openrtb2.BidRequest{User: &openrtb2.User{ID: "UserIdIn", BuyerUID: "12345"}},
			expectedRequest: openrtb2.BidRequest{User: &openrtb2.User{ID: "UserId", BuyerUID: "12345"}, Site: &openrtb2.Site{ID: "SiteId"}, App: &openrtb2.App{ID: "AppId"}},
		},
		{
			description:     "req.User is defined; bidderFPD.User defined; req.User has BuyerUID with zero length. Expect to see empty user.BuyerUID in result request",
			inputFpd:        firstpartydata.ResolvedFirstPartyData{Site: &openrtb2.Site{ID: "SiteId"}, App: &openrtb2.App{ID: "AppId"}, User: &openrtb2.User{ID: "UserId"}},
			inputRequest:    openrtb2.BidRequest{User: &openrtb2.User{ID: "UserIdIn", BuyerUID: ""}},
			expectedRequest: openrtb2.BidRequest{User: &openrtb2.User{ID: "UserId"}, Site: &openrtb2.Site{ID: "SiteId"}, App: &openrtb2.App{ID: "AppId"}},
		},
		{
			description:     "req.User is not defined; bidderFPD.User defined and has BuyerUID. Expect to see user.BuyerUID in result request",
			inputFpd:        firstpartydata.ResolvedFirstPartyData{Site: &openrtb2.Site{ID: "SiteId"}, App: &openrtb2.App{ID: "AppId"}, User: &openrtb2.User{ID: "UserId", BuyerUID: "FPDBuyerUID"}},
			inputRequest:    openrtb2.BidRequest{},
			expectedRequest: openrtb2.BidRequest{Site: &openrtb2.Site{ID: "SiteId"}, App: &openrtb2.App{ID: "AppId"}, User: &openrtb2.User{ID: "UserId", BuyerUID: "FPDBuyerUID"}},
		},
	}

	for _, testCase := range testCases {
		applyFPD(&testCase.inputFpd, &testCase.inputRequest)
		assert.Equal(t, testCase.expectedRequest, testCase.inputRequest, fmt.Sprintf("incorrect request after applying fpd, testcase %s", testCase.description))
	}
}

func Test_parseAliasesGVLIDs(t *testing.T) {
	type args struct {
		orig *openrtb2.BidRequest
	}
	tests := []struct {
		name      string
		args      args
		want      map[string]uint16
		wantError bool
	}{
		{
			"AliasGVLID Parsed Correctly",
			args{
				orig: &openrtb2.BidRequest{
					Ext: json.RawMessage(`{"prebid":{"aliases":{"brightroll":"appnexus"}, "aliasgvlids":{"brightroll":1}}}`),
				},
			},
			map[string]uint16{"brightroll": 1},
			false,
		},
		{
			"AliasGVLID parsing error",
			args{
				orig: &openrtb2.BidRequest{
					Ext: json.RawMessage(`{"prebid":{"aliases":{"brightroll":"appnexus"}, "aliasgvlids": {"brightroll":"abc"}`),
				},
			},
			nil,
			true,
		},
		{
			"Invalid AliasGVLID",
			args{
				orig: &openrtb2.BidRequest{
					Ext: json.RawMessage(`{"prebid":{"aliases":{"brightroll":"appnexus"}, "aliasgvlids":"abc"}`),
				},
			},
			nil,
			true,
		},
		{
			"Missing AliasGVLID",
			args{
				orig: &openrtb2.BidRequest{
					Ext: json.RawMessage(`{"prebid":{"aliases":{"brightroll":"appnexus"}}`),
				},
			},
			nil,
			false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseAliasesGVLIDs(tt.args.orig)
			assert.Equal(t, tt.want, got, "parseAliasesGVLIDs() got = %v, want %v", got, tt.want)
			if !tt.wantError && err != nil {
				t.Errorf("parseAliasesGVLIDs() expected error got nil")
			}
		})
	}
}

func TestBuildExtData(t *testing.T) {
	testCases := []struct {
		description string
		input       []byte
		expectedRes string
	}{
		{
			description: "Input object with int value",
			input:       []byte(`{"someData": 123}`),
			expectedRes: `{"data": {"someData": 123}}`,
		},
		{
			description: "Input object with bool value",
			input:       []byte(`{"someData": true}`),
			expectedRes: `{"data": {"someData": true}}`,
		},
		{
			description: "Input object with string value",
			input:       []byte(`{"someData": "true"}`),
			expectedRes: `{"data": {"someData": "true"}}`,
		},
		{
			description: "No input object",
			input:       []byte(`{}`),
			expectedRes: `{"data": {}}`,
		},
		{
			description: "Input object with object value",
			input:       []byte(`{"someData": {"moreFpdData": "fpddata"}}`),
			expectedRes: `{"data": {"someData": {"moreFpdData": "fpddata"}}}`,
		},
	}

	for _, test := range testCases {
		actualRes := WrapJSONInData(test.input)
		assert.JSONEq(t, test.expectedRes, string(actualRes), "Incorrect result data")
	}
}

func TestCleanOpenRTBRequestsFilterBidderRequestExt(t *testing.T) {
	testCases := []struct {
		desc      string
		inExt     json.RawMessage
		inCfgABC  *openrtb_ext.ExtAlternateBidderCodes
		wantExt   []json.RawMessage
		wantError bool
	}{
		{
			desc:     "Nil request ext, default account alternatebiddercodes config (nil)",
			inExt:    nil,
			inCfgABC: nil,
			wantExt: []json.RawMessage{
				json.RawMessage(""),
				json.RawMessage(""),
			},
			wantError: false,
		},
		{
			desc:     "Nil request ext, default account alternatebiddercodes config (explicity defined)",
			inExt:    nil,
			inCfgABC: &openrtb_ext.ExtAlternateBidderCodes{Enabled: false},
			wantExt: []json.RawMessage{
				json.RawMessage(`{"prebid":{"alternatebiddercodes":{"enabled":false,"bidders":null}}}`),
				json.RawMessage(`{"prebid":{"alternatebiddercodes":{"enabled":false,"bidders":null}}}`),
			},
			wantError: false,
		},
		{
			desc:     "request ext, default account alternatebiddercodes config (explicity defined)",
			inExt:    json.RawMessage(`{"prebid":{}}`),
			inCfgABC: &openrtb_ext.ExtAlternateBidderCodes{Enabled: false},
			wantExt: []json.RawMessage{
				json.RawMessage(`{"prebid":{"alternatebiddercodes":{"enabled":false,"bidders":null}}}`),
				json.RawMessage(`{"prebid":{"alternatebiddercodes":{"enabled":false,"bidders":null}}}`),
			},
			wantError: false,
		},
		{
			desc:  "Nil request ext, account alternatebiddercodes config disabled with biddercodes defined",
			inExt: nil,
			inCfgABC: &openrtb_ext.ExtAlternateBidderCodes{
				Enabled: false,
				Bidders: map[string]openrtb_ext.ExtAdapterAlternateBidderCodes{
					"pubmatic": {Enabled: true},
				},
			},
			wantExt: []json.RawMessage{
				json.RawMessage(`{"prebid":{"alternatebiddercodes":{"enabled":false,"bidders":null}}}`),
				json.RawMessage(`{"prebid":{"alternatebiddercodes":{"enabled":false,"bidders":{"pubmatic":{"enabled":true,"allowedbiddercodes":null}}}}}`),
			},
			wantError: false,
		},
		{
			desc:  "Nil request ext, account alternatebiddercodes config disabled with biddercodes defined (not participant bidder)",
			inExt: nil,
			inCfgABC: &openrtb_ext.ExtAlternateBidderCodes{
				Enabled: false,
				Bidders: map[string]openrtb_ext.ExtAdapterAlternateBidderCodes{
					"ix": {Enabled: true},
				},
			},
			wantExt: []json.RawMessage{
				json.RawMessage(`{"prebid":{"alternatebiddercodes":{"enabled":false,"bidders":null}}}`),
				json.RawMessage(`{"prebid":{"alternatebiddercodes":{"enabled":false,"bidders":null}}}`),
			},
			wantError: false,
		},
		{
			desc:     "Nil request ext, alternatebiddercodes config enabled but bidder not present",
			inExt:    nil,
			inCfgABC: &openrtb_ext.ExtAlternateBidderCodes{Enabled: true},
			wantExt: []json.RawMessage{
				json.RawMessage(`{"prebid":{"alternatebiddercodes":{"enabled":true,"bidders":null}}}`),
				json.RawMessage(`{"prebid":{"alternatebiddercodes":{"enabled":true,"bidders":null}}}`),
			},
			wantError: false,
		},
		{
			desc:     "request ext with default alternatebiddercodes values (nil)",
			inExt:    json.RawMessage(`{"prebid":{}}`),
			inCfgABC: nil,
			wantExt: []json.RawMessage{
				json.RawMessage(`{"prebid":{}}`),
				json.RawMessage(`{"prebid":{}}`),
			},
			wantError: false,
		},
		{
			desc:     "request ext w/o alternatebiddercodes",
			inExt:    json.RawMessage(`{"prebid":{}}`),
			inCfgABC: &openrtb_ext.ExtAlternateBidderCodes{},
			wantExt: []json.RawMessage{
				json.RawMessage(`{"prebid":{"alternatebiddercodes":{"enabled":false,"bidders":null}}}`),
				json.RawMessage(`{"prebid":{"alternatebiddercodes":{"enabled":false,"bidders":null}}}`),
			},
			wantError: false,
		},
		{
			desc:     "request ext having alternatebiddercodes for only one bidder",
			inExt:    json.RawMessage(`{"prebid":{"alternatebiddercodes":{"enabled":true,"bidders":{"pubmatic":{"enabled":true,"allowedbiddercodes":["groupm"]}}}}}`),
			inCfgABC: &openrtb_ext.ExtAlternateBidderCodes{Enabled: false},
			wantExt: []json.RawMessage{
				json.RawMessage(`{"prebid":{"alternatebiddercodes":{"enabled":true,"bidders":null}}}`),
				json.RawMessage(`{"prebid":{"alternatebiddercodes":{"enabled":true,"bidders":{"pubmatic":{"enabled":true,"allowedbiddercodes":["groupm"]}}}}}`),
			},
			wantError: false,
		},
		{
			desc:     "request ext having alternatebiddercodes for multiple bidder",
			inExt:    json.RawMessage(`{"prebid":{"alternatebiddercodes":{"enabled":true,"bidders":{"pubmatic":{"enabled":true,"allowedbiddercodes":["groupm"]},"appnexus":{"enabled":true,"allowedbiddercodes":["ix"]}}}}}`),
			inCfgABC: &openrtb_ext.ExtAlternateBidderCodes{Enabled: false},
			wantExt: []json.RawMessage{
				json.RawMessage(`{"prebid":{"alternatebiddercodes":{"enabled":true,"bidders":{"appnexus":{"enabled":true,"allowedbiddercodes":["ix"]}}}}}`),
				json.RawMessage(`{"prebid":{"alternatebiddercodes":{"enabled":true,"bidders":{"pubmatic":{"enabled":true,"allowedbiddercodes":["groupm"]}}}}}`),
			},
			wantError: false,
		},
		{
			desc:     "request ext having alternatebiddercodes for multiple bidder (config alternatebiddercodes not defined)",
			inExt:    json.RawMessage(`{"prebid":{"alternatebiddercodes":{"enabled":true,"bidders":{"pubmatic":{"enabled":true,"allowedbiddercodes":["groupm"]},"appnexus":{"enabled":true,"allowedbiddercodes":["ix"]}}}}}`),
			inCfgABC: &openrtb_ext.ExtAlternateBidderCodes{Enabled: false},
			wantExt: []json.RawMessage{
				json.RawMessage(`{"prebid":{"alternatebiddercodes":{"enabled":true,"bidders":{"appnexus":{"enabled":true,"allowedbiddercodes":["ix"]}}}}}`),
				json.RawMessage(`{"prebid":{"alternatebiddercodes":{"enabled":true,"bidders":{"pubmatic":{"enabled":true,"allowedbiddercodes":["groupm"]}}}}}`),
			},
			wantError: false,
		},
		{
			desc:  "Nil request ext, alternatebiddercodes config enabled with bidder code for only one bidder",
			inExt: nil,
			inCfgABC: &openrtb_ext.ExtAlternateBidderCodes{
				Enabled: true,
				Bidders: map[string]openrtb_ext.ExtAdapterAlternateBidderCodes{
					"pubmatic": {
						Enabled:            true,
						AllowedBidderCodes: []string{"groupm"},
					},
				},
			},
			wantExt: []json.RawMessage{
				json.RawMessage(`{"prebid":{"alternatebiddercodes":{"enabled":true,"bidders":null}}}`),
				json.RawMessage(`{"prebid":{"alternatebiddercodes":{"enabled":true,"bidders":{"pubmatic":{"enabled":true,"allowedbiddercodes":["groupm"]}}}}}`),
			},
			wantError: false,
		},
	}

	for _, test := range testCases {
		req := newBidRequestWithBidderParams(t)
		req.Ext = nil
		var extRequest *openrtb_ext.ExtRequest
		if test.inExt != nil {
			req.Ext = test.inExt
			unmarshaledExt, err := extractBidRequestExt(req)
			assert.NoErrorf(t, err, test.desc+":Error unmarshaling inExt")
			extRequest = unmarshaledExt
		}

		auctionReq := AuctionRequest{
			BidRequestWrapper: &openrtb_ext.RequestWrapper{BidRequest: req},
			UserSyncs:         &emptyUsersync{},
			Account:           config.Account{AlternateBidderCodes: test.inCfgABC},
		}
		gdprPermissionsBuilder := fakePermissionsBuilder{
			permissions: &permissionsMock{
				allowAllBidders: true,
			},
		}.Builder
		tcf2ConfigBuilder := fakeTCF2ConfigBuilder{
			cfg: gdpr.NewTCF2Config(config.TCF2{}, config.AccountGDPR{}),
		}.Builder

		reqSplitter := &requestSplitter{
			bidderToSyncerKey: map[string]string{},
			me:                &metrics.MetricsEngineMock{},
			privacyConfig:     config.Privacy{},
			gdprPermsBuilder:  gdprPermissionsBuilder,
			tcf2ConfigBuilder: tcf2ConfigBuilder,
			hostSChainNode:    nil,
			bidderInfo:        config.BidderInfos{},
		}

		bidderRequests, _, errs := reqSplitter.cleanOpenRTBRequests(context.Background(), auctionReq, extRequest, gdpr.SignalNo)
		assert.Equal(t, test.wantError, len(errs) != 0, test.desc)
		sort.Slice(bidderRequests, func(i, j int) bool {
			return bidderRequests[i].BidderCoreName < bidderRequests[j].BidderCoreName
		})
		for i, wantBidderRequest := range test.wantExt {
			assert.Equal(t, wantBidderRequest, bidderRequests[i].BidRequest.Ext, test.desc+" : "+string(bidderRequests[i].BidderCoreName)+"\n\t\tGotRequestExt : "+string(bidderRequests[i].BidRequest.Ext))
		}
	}
}
