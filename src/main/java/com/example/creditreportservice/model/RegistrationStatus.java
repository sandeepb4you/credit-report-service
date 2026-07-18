package com.example.creditreportservice.model;

/**
 * Lifecycle of a registration attempt.
 *
 * <ul>
 *   <li>{@link #STARTED} — OTP send requested, awaiting verification.</li>
 *   <li>{@link #OTP_VERIFIED} — OTP accepted, awaiting PAN submission.</li>
 *   <li>{@link #PAN_VERIFIED} — PAN + OCR accepted, {@code User} row created. Terminal success.</li>
 *   <li>{@link #EXPIRED} — OTP window elapsed. Terminal failure.</li>
 * </ul>
 */
public enum RegistrationStatus {
    STARTED,
    OTP_VERIFIED,
    PAN_VERIFIED,
    EXPIRED
}
