# Accessibility Guidelines

User-facing content is handled by WordPress.com:
- Email notifications (templates in wp-content/mu-plugins/html-emails/)
- Error messages shown in Jetpack dashboard
- Activity Log entries
- REST API responses

No accessibility concerns apply because:
- No HTML/UI rendered by this service
- No visual elements
- No interactive components
- All communication is API-based JSON

This is purely infrastructure code with no internationalization (i18n) or accessibility (a11y) requirements. If at some point in the future this should change, here are specific requirements you must be aware of:

- Use semantic HTML
- Use proper ARIA labels in all interfaces
- Use keyboard navigation support
- Use predictable focus management, especially with regards to dialogs and dynamic content changes
- Ensure dynamic content changes are propertly surfaced to screen readers
- Use sufficient color contrast (4.5:1 minimum)
- Use meaningful alternative text for non-text content
