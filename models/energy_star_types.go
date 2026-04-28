package models

import (
	"encoding/json"
	"fmt"
)

// FlexString unmarshals a JSON value that may be either a quoted string or a
// bare number (e.g. a Unix-ms timestamp). In both cases it stores the raw
// value as a plain Go string so downstream code is unaffected.
type FlexString string

func (f *FlexString) UnmarshalJSON(b []byte) error {
	if len(b) == 0 {
		return nil
	}
	// Quoted string — strip the quotes via standard unmarshalling.
	if b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		*f = FlexString(s)
		return nil
	}
	// Bare number (or null) — store the raw bytes as a string.
	if string(b) == "null" {
		*f = ""
		return nil
	}
	*f = FlexString(fmt.Sprintf("%s", b))
	return nil
}

// EnergyStarSearchResponse is the top-level response from the Energy Star
// rebate-finder search API.
type EnergyStarSearchResponse struct {
	ResultsCount int                   `json:"resultsCount"`
	PageSize     int                   `json:"pageSize"`
	Results      []EnergyStarRawResult `json:"results"`
}

// EnergyStarRawResult is one row from the results array.
// IncentiveData is a stringified JSON blob that must be parsed separately with
// a second json.Unmarshal call.
type EnergyStarRawResult struct {
	IncentiveID          string `json:"incentive_id"`
	PublishedIncentiveID string `json:"publishedincentiveid"`
	Utility              string `json:"utility"`
	ZipCode              string `json:"zip_code"`
	AvailableNationwide  string `json:"available_nationwide"` // "Yes" / "No"
	PartnerCategory      string `json:"partner_category"`
	ProductCategory      string `json:"product_category"`
	ProductGeneral       string `json:"product_general"`
	Product              string `json:"product"` // subcategory / "All"
	IncentiveAmount      string `json:"incentiveamount"`
	IncentiveStartDate   FlexString `json:"incentive_start_date"` // Unix ms — may arrive as string or number
	IncentiveEndDate     FlexString `json:"incentive_end_date"`   // Unix ms — may arrive as string or number
	IncentiveData        string `json:"incentivedata"`        // stringified JSON
}

// EnergyStarIncentiveData is the parsed form of the incentivedata field.
type EnergyStarIncentiveData struct {
	ServiceTerritory        *ESTServiceTerritory   `json:"serviceterritory"`
	IncentiveType           *ESTNamedEntity        `json:"incentivetype"`
	IncentiveAmount         string                 `json:"incentiveamount"`
	IncentiveMarketSector   *ESTNamedEntity        `json:"incentivemarketsector"`
	IncentiveBuildingSector *ESTNamedEntity        `json:"incentivebuildingsector"`
	IncentiveRecipient      *ESTNamedEntity        `json:"incentiverecipient"`
	IncomeQualification     *ESTNamedEntity        `json:"incomequalification"`
	EnergyAuditRequired     string                 `json:"energyauditrequired"` // "Y" / "N"
	DeliveryMechanics       json.RawMessage        `json:"incentivedeliverymechanics"`
	ProgramWebAddress       string                 `json:"programwebaddress"`
	ContactEmail            string                 `json:"contactemail"`
	ContactPhone            string                 `json:"contactphonenumber"`
	IncentiveStatus         *ESTIncentiveStatus    `json:"incentivestatus"`
	StartDate               FlexString             `json:"incentivestartedate"` // Unix ms — may arrive as string or number
	EndDate                 FlexString             `json:"incentiveenddate"`    // Unix ms — may arrive as string or number
	ProductSubcategory      *ESTProductSubcategory `json:"incentiveproductsubcategory"`
	WebsiteVisibility       *ESTNamedEntity        `json:"websitevisibility"`
	IncentiveComments       json.RawMessage        `json:"incentivecomments"`
}

// ESTServiceTerritory describes the utility service territory.
type ESTServiceTerritory struct {
	Name      string          `json:"serviceterritoryname"`
	StateCode string          `json:"serviceterritorystatecode"`
	Type      *ESTNamedEntity `json:"serviceterritorytype"`
	Desc      string          `json:"serviceterritorydesc"`
}

// ESTIncentiveStatus holds the incentive's publish/active status.
type ESTIncentiveStatus struct {
	Name         string          `json:"incentivestatusname"`
	ActiveStatus *ESTNamedEntity `json:"incentiveactivestatus"`
}

// ESTProductSubcategory describes the product sub-category for the incentive.
type ESTProductSubcategory struct {
	Name     string             `json:"incentiveproductsubcategoryname"`
	Override string             `json:"incentiveproductsubcategoryoverride"`
	General  *ESTProductGeneral `json:"incentiveproductgeneral"`
}

// ESTProductGeneral holds the general product name.
type ESTProductGeneral struct {
	Name string `json:"incentiveproductgeneralname"`
}

// ESTNamedEntity is a generic named lookup entity.  The API reuses similar
// shapes for many fields, each with a slightly different name key.  We use a
// flexible struct that handles the common variants.
type ESTNamedEntity struct {
	// incentivetype / incomequalification / etc. use these key names:
	Name string `json:"incentivetypename,omitempty"`

	// incomequalification uses:
	IncomeQualName string `json:"incomequalificationname,omitempty"`

	// incentivemarketsector uses:
	MarketSectorName string `json:"incentivemarketsectorname,omitempty"`

	// incentivebuildingsector uses:
	BuildingSectorName string `json:"incentivebuildingsectorname,omitempty"`

	// incentiverecipient uses:
	RecipientName string `json:"incentiverecipientname,omitempty"`

	// incentivestatus / incentiveactivestatus uses:
	ActiveStatusName string `json:"incentiveactivestatusname,omitempty"`

	// websitevisibility uses:
	VisibilityName string `json:"websitevisibilityname,omitempty"`

	// serviceterritorytype uses:
	TerritoryTypeName string `json:"serviceterritorytypename,omitempty"`
}

// BestName returns the first non-empty name field across all known key variants.
func (e *ESTNamedEntity) BestName() string {
	if e == nil {
		return ""
	}
	for _, s := range []string{
		e.Name,
		e.IncomeQualName,
		e.MarketSectorName,
		e.BuildingSectorName,
		e.RecipientName,
		e.ActiveStatusName,
		e.VisibilityName,
		e.TerritoryTypeName,
	} {
		if s != "" {
			return s
		}
	}
	return ""
}

// ESTDeliveryMechanic is one element of the incentivedeliverymechanics array.
type ESTDeliveryMechanic struct {
	Name string          `json:"incentivedeliverymechanicsname"`
	Type *ESTNamedEntity `json:"incentivetype"`
}
