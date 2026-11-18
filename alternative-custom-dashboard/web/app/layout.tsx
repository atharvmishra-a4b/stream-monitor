import type { Metadata } from "next";
import { Schibsted_Grotesk } from "next/font/google";
import "./globals.css";

const font = Schibsted_Grotesk({ subsets: ["latin"], variable: "--font-ui", display: "swap" });

export const metadata: Metadata = { title: "Stream monitor", description: "Health of every live-streaming laptop, by hub and temple." };

export default function RootLayout({ children }: { children: React.ReactNode }) {
  return (
    <html lang="en" className={font.variable}>
      <body>{children}</body>
    </html>
  );
}
