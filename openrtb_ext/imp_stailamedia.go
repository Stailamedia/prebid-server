package openrtb_ext

import "encoding/json"

// ExtImpStailamedia defines the contract for bidrequest.imp[i].ext.prebid.bidder.stailamedia
type ExtImpStailamedia struct {
	LegacyPlacementId       int                        `json:"placementId"`
	LegacyInvCode           string                     `json:"invCode"`
	LegacyTrafficSourceCode string                     `json:"trafficSourceCode"`
	PlacementId             int                        `json:"placement_id"`
	InvCode                 string                     `json:"inv_code"`
	Member                  string                     `json:"member"`
	Keywords                []*ExtImpStailamediaKeyVal `json:"keywords"`
	TrafficSourceCode       string                     `json:"traffic_source_code"`
	Reserve                 float64                    `json:"reserve"`
	Position                string                     `json:"position"`
	UsePmtRule              *bool                      `json:"use_pmt_rule"`
	// At this time we do no processing on the private sizes, so just leaving it as a JSON blob.
	PrivateSizes json.RawMessage `json:"private_sizes"`
	AdPodId      bool            `json:"generate_ad_pod_id"`
}

// ExtImpStailamediaKeyVal defines the contract for bidrequest.imp[i].ext.prebid.bidder.stailamedia.keywords[i]
type ExtImpStailamediaKeyVal struct {
	Key    string   `json:"key,omitempty"`
	Values []string `json:"value,omitempty"`
}
