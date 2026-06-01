package drive

const (
	MimeFolder = "application/vnd.google-apps.folder"
	MimeDoc    = "application/vnd.google-apps.document"
)

// File mirrors the subset of the Drive v3 files resource we read. JSON tag
// names match the Drive API. Drive returns RFC 3339 timestamps as strings;
// we keep them as strings here and parse downstream.
type File struct {
	ID           string  `json:"id"`
	Name         string  `json:"name"`
	MimeType     string  `json:"mimeType"`
	ModifiedTime string  `json:"modifiedTime,omitempty"`
	Owners       []Owner `json:"owners,omitempty"`
	Trashed      bool    `json:"trashed,omitempty"`
}

type Owner struct {
	EmailAddress string `json:"emailAddress"`
	DisplayName  string `json:"displayName,omitempty"`
}

// FileList is the wire shape of files.list responses.
type FileList struct {
	Files         []File `json:"files"`
	NextPageToken string `json:"nextPageToken,omitempty"`
}

// PrimaryOwnerEmail returns the lowercase email of the first owner, or "" if
// the file has no owners (e.g. shared drives). Drive's "registered" check
// requires this match against the grantee's owner_email.
func (f *File) PrimaryOwnerEmail() string {
	if len(f.Owners) == 0 {
		return ""
	}
	return f.Owners[0].EmailAddress
}
