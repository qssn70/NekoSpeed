package com.nekospeed.android.ui.components

import org.junit.Assert.assertEquals
import org.junit.Test

class SpeedFormatTest {
    @Test
    fun `format speed as decimal Mbps`() {
        assertEquals("0.00 Mbps", formatSpeed(0.0))
        assertEquals("0.00 Mbps", formatSpeed(-1.0))
        assertEquals("1.00 Mbps", formatSpeed(125_000.0))
        assertEquals("8.39 Mbps", formatSpeed(1_048_576.0))
        assertEquals("1000.00 Mbps", formatSpeed(125_000_000.0))
    }
}
