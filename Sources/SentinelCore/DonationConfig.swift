import Foundation

// MARK: - Donations
//
// Sentinel VMS is free — no subscriptions, no per-camera charges. If users want
// to support development they can donate via a Stripe Payment Link. This is the
// single source of truth for that URL (also mirrored in the marketing site).
public enum DonationConfig {
    /// Stripe Payment Link for optional donations ("Support Sentinel VMS",
    /// customer-chosen amount). Mirrored on the marketing site's pricing page.
    public static let url = URL(string: "https://donate.stripe.com/6oUfZj2Rg74u2pU6CT2ZO00")!

    /// Whether the donate URL has been configured (vs. the placeholder).
    public static var isConfigured: Bool {
        !url.absoluteString.contains("REPLACE_WITH_YOUR_PAYMENT_LINK")
    }
}
