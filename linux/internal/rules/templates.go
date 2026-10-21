package rules

// Template categories, in the order the Rules page groups them.
const (
	CategorySecurity   = "security"
	CategoryHealth     = "health"
	CategoryOperations = "operations"
)

// Template is a ready-made rule to start from (WAVE14-AI-ASSIST.md §2). Using
// one only prefills the rule editor: nothing is saved until the admin saves
// it. Rule watches every camera and has no time profile.
type Template struct {
	ID       string
	Category string
	Title    string
	Summary  string
	Rule     Rule
}

// Templates is the catalog, built fresh on each call so a caller can edit
// what it gets back. Suggested() stays the dashboard's empty-state offer.
func Templates() []Template {
	list := []Template{
		{ID: "after-hours-people", Category: CategorySecurity, Title: "After-hours people",
			Summary: "Notifies your phone when a person is seen between 18:00 and 07:00.",
			Rule: Rule{Kinds: []string{"Person"}, MinSeverity: "Warning", Delivery: DeliveryPush, Schedule: true, FromMinute: 18 * 60, ToMinute: 7 * 60,
				Instructions: "Open the live view and check the person. If they are expected, close the alarm. If not, call the site contact."}},
		{ID: "loitering-entrance", Category: CategorySecurity, Title: "Loitering",
			Summary: "Notifies your phone when someone lingers in view. Choose the entrance cameras.",
			Rule: Rule{Kinds: []string{"Loitering"}, MinSeverity: "Warning", Delivery: DeliveryPush,
				Instructions: "Open the live view and check who is lingering. If they have a reason to be there, close the alarm. If not, call the site contact."}},
		{ID: "vehicle-night", Category: CategorySecurity, Title: "Vehicles at night",
			Summary: "Shows vehicles seen between 22:00 and 06:00 on the dashboard.",
			Rule: Rule{Kinds: []string{"Vehicle"}, MinSeverity: "Warning", Delivery: DeliveryDashboard, Schedule: true, FromMinute: 22 * 60, ToMinute: 6 * 60,
				Instructions: "Play the clip and check the vehicle. If it is expected, close the alarm. If not, lock the clip as evidence and call the site contact."}},
		{ID: "line-crossing-intrusion", Category: CategorySecurity, Title: "Line crossing or intrusion",
			Summary: "Notifies your phone when someone crosses a line or enters an area drawn on the camera.",
			Rule: Rule{Kinds: []string{"Line crossing", "Intrusion"}, MinSeverity: "Critical", Delivery: DeliveryPush,
				Instructions: "Open the live view right away and check who crossed. If it is a break-in, call the police and the site contact, and lock the clip as evidence."}},
		{ID: "tamper", Category: CategorySecurity, Title: "Camera tampering",
			Summary: "Notifies your phone when a camera is covered, moved or blinded.",
			Rule: Rule{Kinds: []string{"Tamper"}, MinSeverity: "Critical", Delivery: DeliveryPush,
				Instructions: "Open the live view and check whether the camera is blocked or moved. Check nearby cameras, then send someone to look at it."}},
		{ID: "camera-offline", Category: CategoryHealth, Title: "Camera offline",
			Summary: "Notifies your phone when a camera stops sending video.",
			Rule: Rule{Kinds: []string{"Camera Offline"}, MinSeverity: "Critical", Delivery: DeliveryPush,
				Instructions: "Check the camera's power and network connection. If it stays down, open a ticket with IT."}},
		{ID: "storage-low", Category: CategoryHealth, Title: "Storage low",
			Summary: "Notifies your phone when the recordings disk is almost full.",
			Rule: Rule{Kinds: []string{"Storage"}, MinSeverity: "Critical", Delivery: DeliveryPush,
				Instructions: "Open Storage and check free space. Free up space or shorten retention before recording stops."}},
		{ID: "loud-noise", Category: CategoryOperations, Title: "Loud noise",
			Summary: "Shows loud or alarming sounds on the dashboard.",
			Rule: Rule{Kinds: []string{"audio"}, MinSeverity: "Warning", Delivery: DeliveryDashboard,
				Instructions: "Play the clip and listen. If it sounds like breaking glass, shouting or an alarm, check the live view and call the site contact."}},
	}
	for i := range list {
		r := &list[i].Rule
		r.Name, r.Enabled = list[i].Title, true
		r.Normalize()
	}
	return list
}
