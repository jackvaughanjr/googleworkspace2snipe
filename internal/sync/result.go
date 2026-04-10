package sync

// Result summarises what happened during a sync run.
type Result struct {
	CheckedOut      int      // seats newly assigned
	NotesUpdated    int      // seats whose notes were updated
	CheckedIn       int      // seats returned for inactive users
	Skipped         int      // users already up to date
	Warnings        int      // users with no matching Snipe-IT account, or API errors
	UsersCreated    int      // new Snipe-IT users created (--create-users)
	UnmatchedEmails []string // source users with no corresponding Snipe-IT account
}
