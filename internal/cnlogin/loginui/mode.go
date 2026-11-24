// SPDX-License-Identifier: GPL-3.0-or-later

package loginui

// Mode is the internal same-executable role flag that launches the pure Go +
// MyGo Native UI login window / welcome banner helper process. It is dispatched
// in cmd/launcher before opening databases, account stores or the main window.
const Mode = "--cnlogin-ui"
