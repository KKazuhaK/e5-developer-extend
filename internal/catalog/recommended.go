package catalog

// RecommendedDelegated is the set of delegated Microsoft Graph permissions an
// app registration should hold for the delegated sign-in modes (device code,
// browser, password). It is the single source for automatic app registration
// and for the documentation. offline_access is structural (it makes the sign-in
// renewable) and is not used by any API.
var RecommendedDelegated = []string{
	"User.Read", "User.ReadBasic.All", "GroupMember.Read.All", "Mail.ReadBasic",
	"MailboxSettings.Read", "Calendars.ReadBasic", "Contacts.Read", "Files.Read",
	"Sites.Read.All", "Notes.Read", "Tasks.Read", "Team.ReadBasic.All", "offline_access",
}

// RecommendedApplication is the set of Microsoft Graph application permissions
// an app registration should hold for the client_secret mode. All of them need
// administrator consent.
var RecommendedApplication = []string{
	"User.Read.All", "Organization.Read.All", "Group.Read.All", "Mail.ReadBasic.All",
	"MailboxSettings.Read", "Calendars.Read", "Contacts.Read", "Files.Read.All",
	"Sites.Read.All", "Tasks.Read.All", "Team.ReadBasic.All",
}
