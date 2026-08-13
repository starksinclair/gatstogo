package domain

import (
	"time"

	"github.com/google/uuid"
)

type ReservedSlug struct {
	Slug string `json:"slug"`
}

type Plant struct {
	ID              uuid.UUID `json:"id"`
	Slug            string    `json:"slug"`
	Name            string    `json:"name"`
	City            *string   `json:"city"`
	Address         *string   `json:"address"`
	Phone           *string   `json:"phone"`
	LogoPath        *string   `json:"logo_path"`
	CustomDomain    *string   `json:"custom_domain"`
	DomainStatus    string    `json:"domain_status"`
	Status          string    `json:"status"`
	Timezone        string    `json:"timezone"`
	PrimaryColor    string    `json:"primary_color"`
	SecondaryColor  string    `json:"secondary_color"`
	ButtonColor     string    `json:"button_color"`
	ButtonTextColor string    `json:"button_text_color"`
	FontFamily      string    `json:"font_family"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

type User struct {
	ID           uuid.UUID `json:"id"`
	PlantID      uuid.UUID `json:"plant_id"`
	Name         string    `json:"name"`
	Phone        string    `json:"phone"`
	Role         string    `json:"role"`
	PinHash      *string   `json:"pin_hash"`
	PasswordHash *string   `json:"password_hash"`
	Active       bool      `json:"active"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

type Price struct {
	ID            uuid.UUID  `json:"id"`
	PlantID       uuid.UUID  `json:"plant_id"`
	PricePerKg    int64      `json:"price_per_kg"`
	EffectiveFrom time.Time  `json:"effective_from"`
	SetBy         *uuid.UUID `json:"set_by"`
	CreatedAt     time.Time  `json:"created_at"`
}

type Shift struct {
	ID              uuid.UUID  `json:"id"`
	PlantID         uuid.UUID  `json:"plant_id"`
	UserID          uuid.UUID  `json:"user_id"`
	DeviceID        *string    `json:"device_id"`
	OpenedAt        time.Time  `json:"opened_at"`
	ClosedAt        *time.Time `json:"closed_at"`
	OpeningCashKobo int64      `json:"opening_cash_kobo"`
	ClosingCashKobo *int64     `json:"closing_cash_kobo"`
	TokensIssued    int        `json:"tokens_issued"`
	TokensReturned  *int       `json:"tokens_returned"`
	Notes           *string    `json:"notes"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
}

type Ticket struct {
	ID                uuid.UUID  `json:"id"`
	PlantID           uuid.UUID  `json:"plant_id"`
	Reference         string     `json:"reference"`
	Code              string     `json:"code"`
	CustomerName      *string    `json:"customer_name"`
	CustomerPhone     *string    `json:"customer_phone"`
	SizeGrams         int        `json:"size_grams"`
	RatePerKg         int64      `json:"rate_per_kg"`
	PriceID           *uuid.UUID `json:"price_id"`
	Amount            int64      `json:"amount"`
	Channel           string     `json:"channel"`
	Status            string     `json:"status"`
	NetGrams          *int       `json:"net_grams"`
	EmptyWeightGrams  *int       `json:"empty_weight_grams"`
	FilledWeightGrams *int       `json:"filled_weight_grams"`
	FilledAt          *time.Time `json:"filled_at"`
	FilledShiftID     *uuid.UUID `json:"filled_shift_id"`
	ConfirmedAt       *time.Time `json:"confirmed_at"`
	PaidAt            *time.Time `json:"paid_at"`
	ExpiresAt         *time.Time `json:"expires_at"`
	SoldBy            *uuid.UUID `json:"sold_by"`
	ShiftID           *uuid.UUID `json:"shift_id"`
	VoidReason        *string    `json:"void_reason"`
	VoidedBy          *uuid.UUID `json:"voided_by"`
	VoidedAt          *time.Time `json:"voided_at"`
	IdempotencyKey    *string    `json:"idempotency_key"`
	CreatedAt         time.Time  `json:"created_at"`
	UpdatedAt         time.Time  `json:"updated_at"`
}

type CashMovement struct {
	ID          uuid.UUID  `json:"id"`
	PlantID     uuid.UUID  `json:"plant_id"`
	ShiftID     uuid.UUID  `json:"shift_id"`
	Kind        string     `json:"kind"`
	AmountKobo  int64      `json:"amount_kobo"`
	CountedKobo *int64     `json:"counted_kobo"`
	TicketID    *uuid.UUID `json:"ticket_id"`
	RecordedBy  *uuid.UUID `json:"recorded_by"`
	Note        *string    `json:"note"`
	CreatedAt   time.Time  `json:"created_at"`
}

type ActivityLog struct {
	ID        int64          `json:"id"`
	PlantID   *uuid.UUID     `json:"plant_id"`
	ActorID   *uuid.UUID     `json:"actor_id"`
	Action    string         `json:"action"`
	Subject   *string        `json:"subject"`
	Detail    map[string]any `json:"detail"`
	CreatedAt time.Time      `json:"created_at"`
}
