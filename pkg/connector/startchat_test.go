package connector

import (
	"context"
	"testing"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
)

type phoneBook struct {
	store.NoopStore
	contacts map[types.JID]types.ContactInfo
	pnForLID map[types.JID]types.JID
}

func (b *phoneBook) GetContact(_ context.Context, jid types.JID) (types.ContactInfo, error) {
	return b.contacts[jid], nil
}

func (b *phoneBook) GetPNForLID(_ context.Context, lid types.JID) (types.JID, error) {
	return b.pnForLID[lid], nil
}

func TestAddressBookName(t *testing.T) {
	pn := types.NewJID("14695551234", types.DefaultUserServer)
	lid := types.NewJID("195799069757505", types.HiddenUserServer)
	firstOnly := types.NewJID("14695550000", types.DefaultUserServer)
	pushOnly := types.NewJID("14695559999", types.DefaultUserServer)
	book := &phoneBook{
		contacts: map[types.JID]types.ContactInfo{
			pn:        {FullName: "Anna Petrova", FirstName: "Anna", PushName: "anna"},
			lid:       {PushName: "anna"},
			firstOnly: {FirstName: "Oleg"},
			pushOnly:  {PushName: "Maria"},
		},
		pnForLID: map[types.JID]types.JID{lid: pn},
	}
	wa := &WhatsAppClient{Client: &whatsmeow.Client{Store: &store.Device{Contacts: book, LIDs: book}}}
	cases := []struct {
		jid  types.JID
		want string
	}{
		{pn, "Anna Petrova"},
		{lid, "Anna Petrova"},
		{firstOnly, "Oleg"},
		{pushOnly, ""},
	}
	for _, c := range cases {
		if got := wa.addressBookName(context.Background(), c.jid); got != c.want {
			t.Errorf("addressBookName(%s) = %q, want %q", c.jid, got, c.want)
		}
	}
}
