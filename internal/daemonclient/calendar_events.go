package daemonclient

import (
	"context"
	"errors"
	"slices"
	"time"

	"go.kenn.io/msgvault/internal/calevent"
	"go.kenn.io/msgvault/internal/store"
	apiclient "go.kenn.io/msgvault/pkg/client"
	"go.kenn.io/msgvault/pkg/client/generated"
)

// ArchivedCalendarPage reads a specific selection of calendars. A daemon that
// ignores the new paging contract cannot return an unfiltered legacy result.
func (c *Client) ArchivedCalendarPage(ctx context.Context, ids []int64, from, to time.Time, limit, offset int64) (*generated.CalendarOccurrencePage, error) {
	if len(ids) == 0 {
		return nil, errors.New("select at least one calendar source")
	}
	params := generated.ListCalendarEventsQuery{From: from, To: to, SourceIds: ids, Limit: &limit, Offset: &offset}
	response, err := APIResponse(c, func(client *apiclient.Client) (*generated.ListCalendarEventsResp, error) {
		return client.ListCalendarEventsWithResponse(ctx, &generated.ListCalendarEventsRequestOptions{Query: &params})
	})
	if err != nil {
		return nil, err
	}
	page := response.JSON200
	if page == nil || page.Total == nil || int64(len(page.Occurrences)+len(page.Unexpandable)) > limit {
		return nil, errors.New("daemon does not support scoped calendar pages")
	}
	for _, occurrence := range page.Occurrences {
		if !slices.Contains(ids, occurrence.SourceID) {
			return nil, errors.New("daemon returned an unselected calendar")
		}
	}
	return page, nil
}

func (c *Client) ListCalendarOccurrencesContext(ctx context.Context, from, to time.Time) (store.CalendarOccurrencePage, error) {
	response, err := APIResponse(c, func(client *apiclient.Client) (*generated.ListCalendarEventsResp, error) {
		return client.ListCalendarEventsWithResponse(ctx, &generated.ListCalendarEventsRequestOptions{Query: &generated.ListCalendarEventsQuery{From: from, To: to}})
	})
	if err != nil {
		return store.CalendarOccurrencePage{}, err
	}
	if response.JSON200 == nil {
		return store.CalendarOccurrencePage{}, errors.New("calendar events: empty response")
	}
	decoded := response.JSON200
	page := store.CalendarOccurrencePage{Occurrences: make([]store.CalendarOccurrence, 0, len(decoded.Occurrences)), Unexpandable: make([]store.CalendarUnexpandable, 0, len(decoded.Unexpandable))}
	for _, o := range decoded.Occurrences {
		attendees := make([]calevent.Attendee, 0, len(o.Attendees))
		for _, a := range o.Attendees {
			attendees = append(attendees, calevent.Attendee{Email: a.Email, DisplayName: a.DisplayName, Role: a.Role, RSVP: a.Rsvp, IsCopySelf: a.IsSelf})
		}
		page.Occurrences = append(page.Occurrences, store.CalendarOccurrence{MessageID: o.MessageID, SeriesMessageID: o.SeriesMessageID, SourceID: o.SourceID, Subject: o.Subject, Start: o.Start, End: o.End, AllDay: o.AllDay, StartDate: o.StartDate, EndDate: o.EndDate, Status: o.Status, DeletedFromSource: o.DeletedFromSource, Attendees: attendees})
	}
	for _, u := range decoded.Unexpandable {
		page.Unexpandable = append(page.Unexpandable, store.CalendarUnexpandable(u))
	}
	return page, nil
}
