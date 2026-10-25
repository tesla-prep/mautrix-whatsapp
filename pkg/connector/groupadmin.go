// mautrix-whatsapp - A Matrix-WhatsApp puppeting bridge.
// Copyright (C) 2026 CIRQLE
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program.  If not, see <https://www.gnu.org/licenses/>.

package connector

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/rs/zerolog"
	"go.mau.fi/util/exhttp"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/id"

	"go.mau.fi/mautrix-whatsapp/pkg/waid"
)

// groupAdminTimeout bounds asking WhatsApp who is in the group and changing the role.
const groupAdminTimeout = 30 * time.Second

var (
	errWhatsAppRefused  = mautrix.MUnknown.WithStatus(http.StatusBadGateway)
	errWhatsAppTimedOut = mautrix.MUnknown.WithStatus(http.StatusGatewayTimeout)
)

// reqGroupAdmin makes a member of a WhatsApp group an admin, or dismisses them as admin (CIRQLE):
//
//	POST /_matrix/provision/v3/cirqle/group_admin?user_id=<owner>&login_id=<login>
//	{"room_id": "!…", "participant": "@whatsapp_…:server", "admin": true}
//
// Matrix power levels can't carry it: the number, as an admin, has the same
// level (50) as an admin it would dismiss, and Matrix only lets a user change
// levels below its own.
type reqGroupAdmin struct {
	RoomID id.RoomID `json:"room_id"`
	// A WhatsApp member's ghost, or the Matrix user of another of the bridge's logins.
	Participant id.UserID `json:"participant"`
	Admin       bool      `json:"admin"`
}

func (wa *WhatsAppConnector) addProvisioningRoutes() {
	matrix, ok := wa.Bridge.Matrix.(bridgev2.MatrixConnectorWithProvisioning)
	if !ok {
		return
	}
	prov := matrix.GetProvisioning()
	prov.GetRouter().HandleFunc("POST /v3/cirqle/group_admin", func(w http.ResponseWriter, r *http.Request) {
		wa.postGroupAdmin(w, r, prov.GetUser(r))
	})
}

func (wa *WhatsAppConnector) postGroupAdmin(w http.ResponseWriter, r *http.Request, user *bridgev2.User) {
	var req reqGroupAdmin
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.RoomID == "" || req.Participant == "" {
		mautrix.MBadJSON.WithMessage("room_id and participant are required").Write(w)
		return
	}
	login := user.GetDefaultLogin()
	if loginID := r.URL.Query().Get("login_id"); loginID != "" {
		login = wa.Bridge.GetCachedUserLoginByID(networkid.UserLoginID(loginID))
		if login != nil && login.UserMXID != user.MXID {
			login = nil
		}
	}
	if login == nil {
		mautrix.MNotFound.WithMessage("Login not found").Write(w)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), groupAdminTimeout)
	defer cancel()
	err := login.Client.(*WhatsAppClient).setGroupAdmin(ctx, req)
	var respErr mautrix.RespError
	if errors.As(err, &respErr) {
		respErr.Write(w)
		return
	} else if err != nil {
		zerolog.Ctx(ctx).Err(err).Msg("Failed to change group admin")
		mautrix.MUnknown.WithMessage("Something went wrong. Try again later.").Write(w)
		return
	}
	exhttp.WriteJSONResponse(w, http.StatusOK, struct{}{})
}

func (wa *WhatsAppClient) setGroupAdmin(ctx context.Context, req reqGroupAdmin) error {
	portal, err := wa.Main.Bridge.GetPortalByMXID(ctx, req.RoomID)
	if err != nil {
		return err
	} else if portal == nil {
		return mautrix.MInvalidParam.WithMessage("This room is not a WhatsApp chat")
	}
	groupJID, err := waid.ParsePortalID(portal.ID)
	if err != nil || groupJID.Server != types.GroupServer {
		return mautrix.MInvalidParam.WithMessage("Only WhatsApp groups have admins")
	}
	target, err := wa.participantJID(ctx, req.Participant)
	if err != nil {
		return err
	} else if wa.IsOwnJID(target) {
		return mautrix.MInvalidParam.WithMessage("The number can't change its own admin role")
	}
	if wa.Client == nil || !wa.Client.IsLoggedIn() {
		return mautrix.MForbidden.WithMessage("The number is not connected to WhatsApp")
	}
	info, err := wa.Client.GetGroupInfo(ctx, groupJID)
	if errors.Is(err, whatsmeow.ErrNotInGroup) || errors.Is(err, whatsmeow.ErrGroupNotFound) {
		return mautrix.MForbidden.WithMessage("The number is not in this group")
	} else if err != nil {
		return whatsAppFailure(err, "WhatsApp didn't say who is in the group")
	}
	var own, member *types.GroupParticipant
	for i, pcp := range info.Participants {
		if wa.IsOwnJID(pcp.JID) || wa.IsOwnJID(pcp.PhoneNumber) || wa.IsOwnJID(pcp.LID) {
			own = &info.Participants[i]
		} else if isParticipant(pcp, target) {
			member = &info.Participants[i]
		}
	}
	if own == nil {
		return mautrix.MForbidden.WithMessage("The number is not in this group")
	} else if !own.IsAdmin {
		return mautrix.MForbidden.WithMessage("The number is not an admin of this group")
	} else if member == nil {
		return mautrix.MInvalidParam.WithMessage("They are not in this WhatsApp group")
	} else if !req.Admin && member.IsSuperAdmin {
		return mautrix.MInvalidParam.WithMessage("The group's creator can't be dismissed as admin")
	}
	change := &events.GroupInfo{JID: groupJID, Sender: &own.JID, Timestamp: time.Now()}
	action := whatsmeow.ParticipantChangeDemote
	if req.Admin {
		action = whatsmeow.ParticipantChangePromote
		change.Promote = []types.JID{member.JID}
	} else {
		change.Demote = []types.JID{member.JID}
	}
	if member.IsAdmin != req.Admin {
		resp, err := wa.Client.UpdateGroupParticipants(ctx, groupJID, []types.JID{member.JID}, action)
		if err != nil {
			return whatsAppFailure(err, "WhatsApp refused")
		} else if len(resp) == 0 {
			return errWhatsAppRefused.WithMessage("WhatsApp didn't answer the change")
		} else if resp[0].Error != 0 {
			return errWhatsAppRefused.WithMessage("WhatsApp refused (code %d)", resp[0].Error)
		}
		zerolog.Ctx(ctx).Info().
			Stringer("group_jid", groupJID).
			Stringer("participant_jid", member.JID).
			Str("change", string(action)).
			Msg("Changed group admin")
	}
	// WhatsApp tells the group as well, but the room shows the new role right
	// away, also when WhatsApp already had it and Matrix didn't.
	wa.handleWAGroupInfoChange(ctx, change)
	return nil
}

// participantJID is who a Matrix user is on WhatsApp: a member's ghost, or one of the bridge's own logins.
func (wa *WhatsAppClient) participantJID(ctx context.Context, userID id.UserID) (types.JID, error) {
	if ghostID, ok := wa.Main.Bridge.Matrix.ParseGhostMXID(userID); ok {
		if jid := waid.ParseUserID(ghostID); !jid.IsEmpty() {
			return jid, nil
		}
	}
	user, err := wa.Main.Bridge.GetExistingUserByMXID(ctx, userID)
	if err != nil {
		return types.EmptyJID, err
	}
	if user != nil {
		for _, login := range user.GetUserLogins() {
			if waLogin, ok := login.Client.(*WhatsAppClient); ok && !waLogin.JID.IsEmpty() {
				return waLogin.JID.ToNonAD(), nil
			}
		}
	}
	return types.EmptyJID, mautrix.MInvalidParam.WithMessage("%s is not on WhatsApp", userID)
}

func isParticipant(pcp types.GroupParticipant, jid types.JID) bool {
	jid = jid.ToNonAD()
	return pcp.JID.ToNonAD() == jid || pcp.PhoneNumber.ToNonAD() == jid || pcp.LID.ToNonAD() == jid
}

// whatsAppFailure tells a WhatsApp refusal (502) from no answer in time (504).
func whatsAppFailure(err error, what string) error {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, whatsmeow.ErrIQTimedOut) {
		return errWhatsAppTimedOut.WithMessage("WhatsApp has not answered. Check the member list in a while.")
	}
	return errWhatsAppRefused.WithMessage("%s: %v", what, err)
}
