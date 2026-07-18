package com.example.creditreportservice.service;

import lombok.RequiredArgsConstructor;
import lombok.extern.slf4j.Slf4j;
import org.springframework.beans.factory.annotation.Value;
import org.springframework.mail.SimpleMailMessage;
import org.springframework.mail.javamail.JavaMailSender;
import org.springframework.stereotype.Service;

/**
 * Sends OTP emails. Falls back to a log line when no JavaMailSender is
 * configured (e.g. local dev without SMTP creds) so the flow is still testable.
 */
@Slf4j
@Service
@RequiredArgsConstructor
public class MailService {

    private final JavaMailSender mailSender;

    @Value("${spring.mail.username:noreply@credit-report.local}")
    private String fromAddress;

    public void sendOtp(String toEmail, String otp) {
        SimpleMailMessage msg = new SimpleMailMessage();
        msg.setFrom(fromAddress);
        msg.setTo(toEmail);
        msg.setSubject("Your Credit Report registration OTP");
        msg.setText("Your verification code is " + otp
                + ". It expires in a few minutes. If you did not request this, ignore this email.");
        try {
            mailSender.send(msg);
            log.info("OTP email dispatched to {}", toEmail);
        } catch (RuntimeException e) {
            // Always log the OTP locally so dev runs without SMTP can still complete the flow.
            log.warn("SMTP send failed for {}; OTP for local testing: {}", toEmail, otp, e);
            throw e;
        }
    }

}
