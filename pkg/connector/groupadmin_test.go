package connector

import (
	"testing"

	"go.mau.fi/whatsmeow/types"
)

func TestIsParticipant(t *testing.T) {
	lid := types.NewJID("195799069757505", types.HiddenUserServer)
	phone := types.NewJID("14695551234", types.DefaultUserServer)
	inLIDGroup := types.GroupParticipant{JID: lid, LID: lid, PhoneNumber: phone}
	inPhoneGroup := types.GroupParticipant{JID: phone, PhoneNumber: phone}
	cases := []struct {
		pcp  types.GroupParticipant
		jid  types.JID
		want bool
	}{
		{inLIDGroup, lid, true},
		{inLIDGroup, phone, true},
		{inLIDGroup, types.NewADJID(phone.User, 0, 12), true},
		{inPhoneGroup, phone, true},
		{inPhoneGroup, lid, false},
		{inLIDGroup, types.NewJID("14695550000", types.DefaultUserServer), false},
	}
	for _, c := range cases {
		if got := isParticipant(c.pcp, c.jid); got != c.want {
			t.Errorf("isParticipant(%v, %v) = %v, want %v", c.pcp.JID, c.jid, got, c.want)
		}
	}
}
