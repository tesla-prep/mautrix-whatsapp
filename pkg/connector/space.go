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

	"github.com/rs/zerolog"
)

// ensureSpaceRoom gives the login its personal filtering space as soon as it
// connects (CIRQLE). bridgev2 only makes the space when the login's first chat
// gets a room, so a number with no chats bridged yet had none, and CIRQLE
// Messenger, which lists numbers by their spaces, did not show it at all
// (2026-10-26: «SMS USA (NY)», connected since 10-23 with no chats).
func (wa *WhatsAppClient) ensureSpaceRoom(ctx context.Context) {
	if _, err := wa.UserLogin.GetSpaceRoom(ctx); err != nil {
		zerolog.Ctx(ctx).Err(err).Msg("Failed to create the login's space")
	}
}
